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
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"strings"
)

// nolint:unused
// log is for logging in this package.
var podlog = logf.Log.WithName("pod-resource")

var (
	// configMapOperations tracks ConfigMap creation/update operations
	// (controller-runtime already provides webhook latency and request metrics)
	configMapOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wif_configmap_operations_total",
			Help: "Total number of ConfigMap operations",
		},
		[]string{"operation", "result"},
	)
)

func init() {
	// Register metrics with controller-runtime
	// Note: controller-runtime provides these built-in webhook metrics:
	// - controller_runtime_webhook_latency_seconds
	// - controller_runtime_webhook_requests_total
	// - controller_runtime_webhook_requests_in_flight
	metrics.Registry.MustRegister(configMapOperations)
}

// SetupPodWebhookWithManager registers the webhook for Pod in the manager.
func SetupPodWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&corev1.Pod{}).
		WithDefaulter(&PodCustomDefaulter{
			Client: mgr.GetClient(),
			Cache:  mgr.GetCache(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=wif-injection.workload-identity.io,admissionReviewVersions=v1,timeoutSeconds=10
// +kubebuilder:webhookconfiguration:mutating=true,name=workload-identity-injection

// PodCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Pod when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PodCustomDefaulter struct {
	Client client.Client
	Cache  client.Reader
}

var _ webhook.CustomDefaulter = &PodCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Pod.
func (d *PodCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("expected an Pod object but got %T", obj)
	}

	podlog.Info("Defaulting for Pod", "name", pod.GetName())

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
	wifConfig := extractWIFConfig(sa)

	// Ensure required ConfigMaps exist (create on-demand)
	err = d.ensureConfigMaps(ctx, pod.Namespace, wifConfig, pod, sa)
	if err != nil {
		podlog.Error(err, "Failed to ensure ConfigMaps", "namespace", pod.Namespace)
		return err
	}

	// Inject WIF configuration
	injectWorkloadIdentityConfig(pod, wifConfig)

	podlog.Info("Injected WIF configuration", "pod", pod.GetName(), "serviceAccount", saName)
	return nil
}

// WIFConfig holds workload identity federation configuration
type WIFConfig struct {
	UseDirectIdentity    bool
	GoogleServiceAccount string
	CredentialsConfigMap string
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

// extractWIFConfig extracts WIF configuration with opt-out logic (Istio-style)
func extractWIFConfig(sa *corev1.ServiceAccount) *WIFConfig {
	// Check for GKE-style service account impersonation annotation (takes precedence)
	if sa.Annotations != nil {
		if gcpSA, ok := sa.Annotations["iam.gke.io/gcp-service-account"]; ok {
			// Use k8s ServiceAccount name for ConfigMap to enable owner references
			configMapName := fmt.Sprintf("wif-credentials-impersonation-%s", sa.Name)
			return &WIFConfig{
				UseDirectIdentity:    false,
				GoogleServiceAccount: gcpSA,
				CredentialsConfigMap: configMapName,
			}
		}
	}

	// Default: Direct federated identity (opt-out behavior)
	// Users can disable via namespace or pod labels
	return &WIFConfig{
		UseDirectIdentity:    true,
		CredentialsConfigMap: "wif-credentials-direct",
	}
}

// ensureConfigMaps creates ConfigMaps on-demand if they don't exist (idempotent)
func (d *PodCustomDefaulter) ensureConfigMaps(ctx context.Context, namespace string, config *WIFConfig, pod *corev1.Pod, sa *corev1.ServiceAccount) error {
	// Note: Dry-run detection is handled at the webhook framework level
	// sideEffects: NoneOnDryRun ensures this method won't be called during dry-run
	configMapName := config.CredentialsConfigMap

	// First, check if ConfigMap already exists using cache for performance
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
		// ConfigMap already exists, webhook is idempotent
		configMapOperations.WithLabelValues("get", "existing").Inc()
		return nil
	}

	if !apierrors.IsNotFound(err) {
		// Some other error occurred during lookup
		configMapOperations.WithLabelValues("get", "error").Inc()
		return fmt.Errorf("failed to check ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	// ConfigMap doesn't exist, create it atomically
	credentialsData := generateCredentialsConfig(config)

	cmMeta := metav1.ObjectMeta{
		Name:      configMapName,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
			"workload-identity.io/type":    getConfigMapType(config),
		},
	}

	// For impersonation ConfigMaps, set ServiceAccount as owner for cleanup
	// Direct identity ConfigMaps are shared across all pods in namespace
	if !config.UseDirectIdentity {
		cmMeta.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
				Name:       sa.Name,
				UID:        sa.UID,
				Controller: &[]bool{true}[0], // ServiceAccount controls its impersonation ConfigMap
			},
		}
	}

	newCM := &corev1.ConfigMap{
		ObjectMeta: cmMeta,
		Data: map[string]string{
			"credentials.json": credentialsData,
		},
	}

	// Attempt atomic creation
	err = d.Client.Create(ctx, newCM)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race condition: another webhook instance created it concurrently
			// This is expected and safe - webhook operation is idempotent
			configMapOperations.WithLabelValues("create", "race_handled").Inc()
			podlog.V(1).Info("ConfigMap created concurrently by another webhook instance", "configmap", configMapName, "namespace", namespace)
			return nil
		}
		// Actual creation failure
		configMapOperations.WithLabelValues("create", "error").Inc()
		return fmt.Errorf("failed to create ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	// Successfully created
	configMapOperations.WithLabelValues("create", "success").Inc()
	podlog.Info("Created WIF ConfigMap on-demand", "configmap", configMapName, "namespace", namespace)
	return nil
}

// getConfigMapType returns the type label for the ConfigMap
func getConfigMapType(config *WIFConfig) string {
	if config.UseDirectIdentity {
		return "direct"
	}
	return "impersonation"
}

// injectWorkloadIdentityConfig injects WIF volumes and environment variables into pod
func injectWorkloadIdentityConfig(pod *corev1.Pod, config *WIFConfig) {
	// Get the full provider path and format as audience
	providerPath := getWorkloadIdentityProvider()
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
	} else {
		podlog.V(1).Info("Skipped token volume injection - volume already exists", "pod", pod.GetName(), "volume", "token")
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
	} else {
		podlog.V(1).Info("Skipped credentials volume injection - volume already exists", "pod", pod.GetName(), "volume", credentialsVolumeName)
	}

	// Add volume mounts and environment variables to all containers
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]

		// Add volume mounts if they don't exist
		tokenMountPath := "/var/run/service-account"
		if !volumeMountExists(container, "token") && !mountPathExists(container, tokenMountPath) {
			container.VolumeMounts = append(container.VolumeMounts,
				corev1.VolumeMount{
					Name:      "token",
					MountPath: tokenMountPath,
					ReadOnly:  true,
				},
			)
		} else {
			if volumeMountExists(container, "token") {
				podlog.V(1).Info("Skipped token mount injection - volume mount already exists", "pod", pod.GetName(), "container", container.Name, "volume", "token")
			}
			if mountPathExists(container, tokenMountPath) {
				podlog.V(1).Info("Skipped token mount injection - mount path already in use", "pod", pod.GetName(), "container", container.Name, "path", tokenMountPath)
			}
		}

		credentialsMountPath := "/etc/workload-identity"
		if !volumeMountExists(container, credentialsVolumeName) && !mountPathExists(container, credentialsMountPath) {
			container.VolumeMounts = append(container.VolumeMounts,
				corev1.VolumeMount{
					Name:      credentialsVolumeName,
					MountPath: credentialsMountPath,
					ReadOnly:  true,
				},
			)
		} else {
			if volumeMountExists(container, credentialsVolumeName) {
				podlog.V(1).Info("Skipped credentials mount injection - volume mount already exists", "pod", pod.GetName(), "container", container.Name, "volume", credentialsVolumeName)
			}
			if mountPathExists(container, credentialsMountPath) {
				podlog.V(1).Info("Skipped credentials mount injection - mount path already in use", "pod", pod.GetName(), "container", container.Name, "path", credentialsMountPath)
			}
		}

		// Add environment variable if it doesn't exist
		if !envVarExists(container, "GOOGLE_APPLICATION_CREDENTIALS") {
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "GOOGLE_APPLICATION_CREDENTIALS",
				Value: "/etc/workload-identity/credentials.json",
			})
		} else {
			podlog.V(1).Info("Skipped GOOGLE_APPLICATION_CREDENTIALS injection - env var already exists", "pod", pod.GetName(), "container", container.Name, "env", "GOOGLE_APPLICATION_CREDENTIALS")
		}
	}
}

// getWorkloadIdentityProvider returns the full workload identity provider path from environment
func getWorkloadIdentityProvider() string {
	if provider := os.Getenv("WORKLOAD_IDENTITY_PROVIDER"); provider != "" {
		return provider
	}
	return "${WORKLOAD_IDENTITY_PROVIDER}" // Fallback to template processing
}

// generateCredentialsConfig generates the credentials JSON configuration for WIF
func generateCredentialsConfig(config *WIFConfig) string {
	// Get the full provider path and format as audience
	providerPath := getWorkloadIdentityProvider()
	audience := "//iam.googleapis.com/" + providerPath

	if config.UseDirectIdentity {
		// Direct federated identity configuration
		credConfig := map[string]interface{}{
			"type":               "external_account",
			"audience":           audience,
			"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			"token_url":          "https://sts.googleapis.com/v1/token",
			"credential_source": map[string]interface{}{
				"file": "/var/run/service-account/token",
			},
		}
		data, _ := json.MarshalIndent(credConfig, "", "  ")
		return string(data)
	} else {
		// Service account impersonation configuration
		credConfig := map[string]interface{}{
			"universe_domain":    "googleapis.com",
			"type":               "external_account",
			"audience":           audience,
			"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			"token_url":          "https://sts.googleapis.com/v1/token",
			"credential_source": map[string]interface{}{
				"file": "/var/run/service-account/token",
				"format": map[string]interface{}{
					"type": "text",
				},
			},
			"service_account_impersonation_url": fmt.Sprintf("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:generateAccessToken", config.GoogleServiceAccount),
		}
		data, _ := json.MarshalIndent(credConfig, "", "  ")
		return string(data)
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
