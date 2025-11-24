/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

import (
	"context"
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/groq/k8s-wif-webhook/internal/wif"
)

// nolint:unused
// log is for logging in this package.
var podlog = logf.Log.WithName("pod-resource")

var (
	// configMapOperations tracks ConfigMap creation operations
	// Updates are handled by controllers, webhook only creates
	configMapOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wif_webhook_configmap_operations_total",
			Help: "Total number of ConfigMap operations in webhook (create only)",
		},
		[]string{"operation", "result"},
	)

	// injectionOperations tracks WIF injection attempts and skipped cases
	injectionOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wif_injection_operations_total",
			Help: "Total number of WIF injection operations",
		},
		[]string{"component", "action", "reason"},
	)
)

func init() {
	// Register metrics with controller-runtime
	// Note: controller-runtime provides these built-in webhook metrics:
	// - controller_runtime_webhook_latency_seconds
	// - controller_runtime_webhook_requests_total
	// - controller_runtime_webhook_requests_in_flight
	metrics.Registry.MustRegister(configMapOperations, injectionOperations)
}

// SetupPodWebhookWithManager registers the webhook for Pod in the manager.
func SetupPodWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).
		WithDefaulter(&PodCustomDefaulter{
			Client:   mgr.GetClient(),
			Cache:    mgr.GetCache(),
			Recorder: mgr.GetEventRecorderFor("k8s-wif-webhook"),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=wif-injection.workload-identity.io,admissionReviewVersions=v1,timeoutSeconds=10
// +kubebuilder:webhookconfiguration:mutating=true,name=workload-identity-injection

// PodCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Pod when those are created.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PodCustomDefaulter struct {
	Client   client.Client
	Cache    client.Reader
	Recorder record.EventRecorder
}

var _ webhook.CustomDefaulter = &PodCustomDefaulter{}

// potentialPodName returns the pod name if available, otherwise a descriptive string
// This mirrors Istio's approach to handling generateName in admission webhooks
func potentialPodName(metadata metav1.ObjectMeta) string {
	if metadata.Name != "" {
		return metadata.Name
	}
	if metadata.GenerateName != "" {
		return metadata.GenerateName + "***** (actual name not yet known)"
	}
	return ""
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Pod.
func (d *PodCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("expected an Pod object but got %T", obj)
	}

	podName := potentialPodName(pod.ObjectMeta)
	podlog.Info("Processing pod for WIF injection", "pod", podName, "namespace", pod.Namespace)

	// Skip if pod already has WIF volumes/env vars
	if hasWorkloadIdentityConfig(pod) {
		return nil
	}

	// Get ServiceAccount to check for WIF annotations
	sa := &corev1.ServiceAccount{}
	saName := pod.Spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}

	// Use cache consistently for reads (best practice)
	reader := d.Cache
	if reader == nil {
		reader = d.Client // Fallback to direct client only if cache not available
	}

	err := reader.Get(ctx, types.NamespacedName{
		Name:      saName,
		Namespace: pod.Namespace,
	}, sa)
	if err != nil {
		if apierrors.IsNotFound(err) {
			podlog.Error(err, "ServiceAccount not found, failing admission", "name", saName, "namespace", pod.Namespace)
			return fmt.Errorf("ServiceAccount %s/%s not found: %w", pod.Namespace, saName, err)
		}
		podlog.Error(err, "Failed to get ServiceAccount, failing admission", "name", saName, "namespace", pod.Namespace)
		return fmt.Errorf("failed to lookup ServiceAccount %s/%s: %w", pod.Namespace, saName, err)
	}

	// Check if WIF injection is disabled (opt-out logic)
	if isWIFInjectionDisabled(ctx, reader, pod, sa) {
		return nil
	}

	// Extract WIF configuration (defaults to direct identity)
	wifConfig := wif.ExtractWIFConfig(sa)

	// Ensure ConfigMaps exist (atomic creation, controllers handle updates)
	err = d.ensureConfigMaps(ctx, pod.Namespace, wifConfig, pod, sa)
	if err != nil {
		podlog.Error(err, "Failed to ensure ConfigMaps", "namespace", pod.Namespace)
		return err
	}

	// Inject WIF configuration
	injectWorkloadIdentityConfig(d, pod, wifConfig)

	podlog.Info("Injected WIF configuration", "pod", podName, "namespace", pod.Namespace, "serviceAccount", saName)
	return nil
}

// hasWorkloadIdentityConfig checks if pod already has WIF configuration
func hasWorkloadIdentityConfig(pod *corev1.Pod) bool {
	// Check for projected token volume
	for _, volume := range pod.Spec.Volumes {
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ServiceAccountToken != nil &&
					strings.Contains(source.ServiceAccountToken.Audience, "iam.googleapis.com") {
					return true
				}
			}
		}
	}

	// Check for GOOGLE_APPLICATION_CREDENTIALS env var
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
				return true
			}
		}
	}

	return false
}

// isWIFInjectionDisabled checks if WIF injection should be skipped (Istio-style opt-out)
func isWIFInjectionDisabled(ctx context.Context, reader client.Reader, pod *corev1.Pod, sa *corev1.ServiceAccount) bool {
	// Check pod-level opt-out label (highest precedence)
	if pod.Labels != nil {
		if inject, exists := pod.Labels["workload-identity.io/inject"]; exists && inject == "false" {
			return true
		}
	}

	// Check namespace-level opt-out label
	namespace := &corev1.Namespace{}
	err := reader.Get(ctx, types.NamespacedName{Name: pod.Namespace}, namespace)
	if err == nil && namespace.Labels != nil {
		if inject, exists := namespace.Labels["workload-identity.io/injection"]; exists && inject == "disabled" {
			return true
		}
	}

	// Default: injection enabled (opt-out behavior)
	return false
}

// ensureConfigMaps atomically creates ConfigMaps if they don't exist (fast path)
// Controllers handle updates and reconciliation
func (d *PodCustomDefaulter) ensureConfigMaps(ctx context.Context, namespace string, config *wif.WIFConfig, pod *corev1.Pod, sa *corev1.ServiceAccount) error {
	configMapName := config.CredentialsConfigMap

	// Check cache first for performance
	reader := d.Cache
	if reader == nil {
		reader = d.Client
	}

	existingCM := &corev1.ConfigMap{}
	err := reader.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: namespace,
	}, existingCM)

	if err == nil {
		// ConfigMap exists - controllers handle updates
		configMapOperations.WithLabelValues("get", "existing").Inc()
		return nil
	}

	if !apierrors.IsNotFound(err) {
		configMapOperations.WithLabelValues("get", "error").Inc()
		return fmt.Errorf("failed to check ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	// ConfigMap doesn't exist - create atomically
	credentialsData := wif.GenerateCredentialsConfig(config)

	cmMeta := metav1.ObjectMeta{
		Name:      configMapName,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
			"workload-identity.io/type":    wif.GetConfigMapType(config),
		},
	}

	// Set owner reference for impersonation ConfigMaps
	if !config.UseDirectIdentity {
		cmMeta.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
				Name:       sa.Name,
				UID:        sa.UID,
				Controller: &[]bool{true}[0],
			},
		}
	}

	newCM := &corev1.ConfigMap{
		ObjectMeta: cmMeta,
		Data: map[string]string{
			"credentials.json": credentialsData,
		},
	}

	// Atomic creation
	err = d.Client.Create(ctx, newCM)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race condition: controller or another webhook instance created it
			configMapOperations.WithLabelValues("create", "race_handled").Inc()
			podlog.V(1).Info("ConfigMap created concurrently", "configmap", configMapName, "namespace", namespace)
			return nil
		}
		configMapOperations.WithLabelValues("create", "error").Inc()
		if d.Recorder != nil {
			d.Recorder.Event(pod, corev1.EventTypeWarning, "ConfigMapCreateFailed",
				fmt.Sprintf("Failed to create WIF ConfigMap '%s': %v", configMapName, err))
		}
		return fmt.Errorf("failed to create ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	configMapOperations.WithLabelValues("create", "success").Inc()
	if d.Recorder != nil {
		cmType := "direct identity"
		if !config.UseDirectIdentity {
			cmType = fmt.Sprintf("impersonation (GCP SA: %s)", config.GoogleServiceAccount)
		}
		d.Recorder.Event(pod, corev1.EventTypeNormal, "ConfigMapCreated",
			fmt.Sprintf("Created WIF ConfigMap '%s' (%s)", configMapName, cmType))
	}
	podlog.Info("Created WIF ConfigMap on-demand", "configmap", configMapName, "namespace", namespace)
	return nil
}

// parseTargetContainerNames parses container names from the inject-container-names annotation
// Returns nil if annotation is not present (meaning inject into all containers)
func parseTargetContainerNames(pod *corev1.Pod) []string {
	if pod.Annotations == nil {
		return nil
	}

	containerNamesAnnotation, exists := pod.Annotations["workload-identity.io/inject-container-names"]
	if !exists || containerNamesAnnotation == "" {
		return nil
	}

	// Split by comma and trim whitespace
	var containerNames []string
	for _, name := range strings.Split(containerNamesAnnotation, ",") {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			containerNames = append(containerNames, trimmed)
		}
	}

	// If no valid container names found, return empty slice to distinguish from nil
	if len(containerNames) == 0 {
		return []string{}
	}

	return containerNames
}

// shouldInjectIntoContainer determines if WIF should be injected into the given container
func shouldInjectIntoContainer(containerName string, targetContainers []string) bool {
	// If no target containers specified, inject into all containers (default behavior)
	if targetContainers == nil {
		return true
	}

	// Check if container name is in the target list
	for _, target := range targetContainers {
		if target == containerName {
			return true
		}
	}

	return false
}

// injectWorkloadIdentityConfig injects WIF volumes and environment variables into pod
func injectWorkloadIdentityConfig(d *PodCustomDefaulter, pod *corev1.Pod, config *wif.WIFConfig) {
	// Parse target container names from annotation
	targetContainers := parseTargetContainerNames(pod)

	// Log which containers will be targeted for injection
	if targetContainers != nil {
		podlog.Info("Targeting specific containers for WIF injection",
			"pod", potentialPodName(pod.ObjectMeta),
			"namespace", pod.Namespace,
			"containers", strings.Join(targetContainers, ","))
	}

	// Get the full provider path and format as audience
	providerPath := wif.GetWorkloadIdentityProvider()
	audience := "//iam.googleapis.com/" + providerPath

	// Use default token expiration (3600 seconds / 1 hour)
	expirationSeconds := int64(3600)

	// Add projected token volume if it doesn't exist
	if !volumeExists(pod, "token") {
		tokenVolume := corev1.Volume{
			Name: "token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          audience,
								ExpirationSeconds: &expirationSeconds,
								Path:              "token",
							},
						},
					},
				},
			},
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, tokenVolume)
		injectionOperations.WithLabelValues("volume", "injected", "success").Inc()
	} else {
		podlog.Info("Skipped WIF injection due to existing volume",
			"pod", potentialPodName(pod.ObjectMeta),
			"namespace", pod.Namespace,
			"component", "volume",
			"volume", "token",
			"reason", "already_exists")
		injectionOperations.WithLabelValues("volume", "skipped", "already_exists").Inc()
		if d != nil && d.Recorder != nil {
			d.Recorder.Event(pod, corev1.EventTypeWarning, "WIFInjectionSkipped",
				"Skipped token volume injection - volume 'token' already exists")
		}
	}

	// Add credentials config volume if it doesn't exist (ConfigMap is created on-demand)
	credentialsVolumeName := "workload-identity-credential-configuration"
	if !volumeExists(pod, credentialsVolumeName) {
		credentialsVolume := corev1.Volume{
			Name: credentialsVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: config.CredentialsConfigMap,
					},
				},
			},
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, credentialsVolume)
		injectionOperations.WithLabelValues("volume", "injected", "success").Inc()
	} else {
		podlog.Info("Skipped WIF injection due to existing volume",
			"pod", potentialPodName(pod.ObjectMeta),
			"namespace", pod.Namespace,
			"component", "volume",
			"volume", credentialsVolumeName,
			"reason", "already_exists")
		injectionOperations.WithLabelValues("volume", "skipped", "already_exists").Inc()
		if d != nil && d.Recorder != nil {
			d.Recorder.Event(pod, corev1.EventTypeWarning, "WIFInjectionSkipped",
				fmt.Sprintf("Skipped credentials volume injection - volume '%s' already exists", credentialsVolumeName))
		}
	}

	// Define mount paths for container injection
	tokenMountPath := "/var/run/service-account"
	credentialsMountPath := "/etc/workload-identity"

	// Add volume mounts and environment variables to targeted init containers
	for i := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[i]
		injectIntoContainer(d, pod, container, targetContainers, tokenMountPath, credentialsVolumeName, credentialsMountPath, "init")
	}

	// Add volume mounts and environment variables to targeted regular containers
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		injectIntoContainer(d, pod, container, targetContainers, tokenMountPath, credentialsVolumeName, credentialsMountPath, "regular")
	}
}

// injectIntoContainer handles injection logic for a single container (init or regular)
func injectIntoContainer(d *PodCustomDefaulter, pod *corev1.Pod, container *corev1.Container, targetContainers []string, tokenMountPath, credentialsVolumeName, credentialsMountPath, containerType string) {
	// Check if this container should receive WIF injection
	if !shouldInjectIntoContainer(container.Name, targetContainers) {
		podlog.V(1).Info("Skipping container - not in target list",
			"pod", potentialPodName(pod.ObjectMeta),
			"namespace", pod.Namespace,
			"container", container.Name,
			"containerType", containerType)
		injectionOperations.WithLabelValues("container", "skipped", "not_targeted").Inc()
		return
	}

	// Add volume mounts if they don't exist
	if !volumeMountExists(container, "token") && !mountPathExists(container, tokenMountPath) {
		container.VolumeMounts = append(container.VolumeMounts,
			corev1.VolumeMount{
				Name:      "token",
				MountPath: tokenMountPath,
				ReadOnly:  true,
			},
		)
		injectionOperations.WithLabelValues("mount", "injected", "success").Inc()
	} else {
		if volumeMountExists(container, "token") {
			podlog.Info("Skipped WIF injection due to existing volume mount",
				"pod", potentialPodName(pod.ObjectMeta),
				"namespace", pod.Namespace,
				"container", container.Name,
				"containerType", containerType,
				"component", "mount",
				"volume", "token",
				"reason", "volume_mount_exists")
			injectionOperations.WithLabelValues("mount", "skipped", "volume_mount_exists").Inc()
		}
		if mountPathExists(container, tokenMountPath) {
			podlog.Info("Skipped WIF injection due to mount path conflict",
				"pod", potentialPodName(pod.ObjectMeta),
				"namespace", pod.Namespace,
				"container", container.Name,
				"containerType", containerType,
				"component", "mount",
				"path", tokenMountPath,
				"reason", "path_conflict")
			injectionOperations.WithLabelValues("mount", "skipped", "path_conflict").Inc()
		}
	}

	if !volumeMountExists(container, credentialsVolumeName) && !mountPathExists(container, credentialsMountPath) {
		container.VolumeMounts = append(container.VolumeMounts,
			corev1.VolumeMount{
				Name:      credentialsVolumeName,
				MountPath: credentialsMountPath,
				ReadOnly:  true,
			},
		)
		injectionOperations.WithLabelValues("mount", "injected", "success").Inc()
	} else {
		if volumeMountExists(container, credentialsVolumeName) {
			podlog.Info("Skipped WIF injection due to existing volume mount",
				"pod", potentialPodName(pod.ObjectMeta),
				"namespace", pod.Namespace,
				"container", container.Name,
				"containerType", containerType,
				"component", "mount",
				"volume", credentialsVolumeName,
				"reason", "volume_mount_exists")
			injectionOperations.WithLabelValues("mount", "skipped", "volume_mount_exists").Inc()
		}
		if mountPathExists(container, credentialsMountPath) {
			podlog.Info("Skipped WIF injection due to mount path conflict",
				"pod", potentialPodName(pod.ObjectMeta),
				"namespace", pod.Namespace,
				"container", container.Name,
				"containerType", containerType,
				"component", "mount",
				"path", credentialsMountPath,
				"reason", "path_conflict")
			injectionOperations.WithLabelValues("mount", "skipped", "path_conflict").Inc()
		}
	}

	// Add environment variable if it doesn't exist
	if !envVarExists(container, "GOOGLE_APPLICATION_CREDENTIALS") {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "GOOGLE_APPLICATION_CREDENTIALS",
			Value: "/etc/workload-identity/credentials.json",
		})
		injectionOperations.WithLabelValues("env", "injected", "success").Inc()
	} else {
		podlog.Info("Skipped WIF injection due to existing environment variable",
			"pod", potentialPodName(pod.ObjectMeta),
			"namespace", pod.Namespace,
			"container", container.Name,
			"containerType", containerType,
			"component", "env",
			"env", "GOOGLE_APPLICATION_CREDENTIALS",
			"reason", "already_exists")
		injectionOperations.WithLabelValues("env", "skipped", "already_exists").Inc()
	}
}

// volumeExists checks if a volume with the given name already exists in the pod
func volumeExists(pod *corev1.Pod, volumeName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == volumeName {
			return true
		}
	}
	return false
}

// volumeMountExists checks if a volume mount with the given name already exists in the container
func volumeMountExists(container *corev1.Container, volumeName string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == volumeName {
			return true
		}
	}
	return false
}

// envVarExists checks if an environment variable with the given name already exists in the container
func envVarExists(container *corev1.Container, envName string) bool {
	for _, env := range container.Env {
		if env.Name == envName {
			return true
		}
	}
	return false
}

// mountPathExists checks if a mount path already exists in the container
func mountPathExists(container *corev1.Container, mountPath string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.MountPath == mountPath {
			return true
		}
	}
	return false
}
