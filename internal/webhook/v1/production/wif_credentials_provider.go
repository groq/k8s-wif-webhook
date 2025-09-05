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

package production

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/groq/k8s-wif-webhook/internal/webhook/v1/config"
)

var wifCredentialsLog = log.Log.WithName("wif-credentials")

// WIFCredentialsProvider provides Workload Identity Federation credentials
// Following Single Responsibility Principle - only handles WIF credential injection
type WIFCredentialsProvider struct {
	client        client.Client
	cache         client.Reader
	recorder      record.EventRecorder
	volumeManager config.VolumeManager
	wifConfig     *config.Config
}

// WIFConfig holds workload identity federation configuration
type WIFConfig struct {
	UseDirectIdentity    bool
	GoogleServiceAccount string
	CredentialsConfigMap string
}

// NewWIFCredentialsProvider creates a new WIF credentials provider
func NewWIFCredentialsProvider(client client.Client, cache client.Reader, recorder record.EventRecorder, volumeManager config.VolumeManager, wifConfig *config.Config) *WIFCredentialsProvider {
	return &WIFCredentialsProvider{
		client:        client,
		cache:         cache,
		recorder:      recorder,
		volumeManager: volumeManager,
		wifConfig:     wifConfig,
	}
}

// GetCredentialType implements CredentialsProvider interface
func (p *WIFCredentialsProvider) GetCredentialType() string {
	return "workload-identity-federation"
}

// RequiresSetup implements CredentialsProvider interface
func (p *WIFCredentialsProvider) RequiresSetup() bool {
	return false // WIF setup is handled by environment configuration
}

// Setup implements CredentialsProvider interface
func (p *WIFCredentialsProvider) Setup() error {
	// Validate WIF configuration
	return p.wifConfig.Validate()
}

// InjectCredentials implements CredentialsProvider interface
func (p *WIFCredentialsProvider) InjectCredentials(pod *corev1.Pod, namespace string) error {
	// Get ServiceAccount for WIF configuration
	sa, err := p.getServiceAccount(context.Background(), pod, namespace)
	if err != nil {
		return fmt.Errorf("failed to get service account: %w", err)
	}

	// Extract WIF configuration from ServiceAccount
	wifConfig := p.extractWIFConfig(sa)

	// Ensure required ConfigMaps exist
	if err := p.ensureConfigMaps(context.Background(), namespace, wifConfig, pod, sa); err != nil {
		return fmt.Errorf("failed to ensure ConfigMaps: %w", err)
	}

	// Inject WIF configuration
	if err := p.injectWIFConfig(pod, wifConfig); err != nil {
		return fmt.Errorf("failed to inject WIF configuration: %w", err)
	}

	wifCredentialsLog.Info("Injected WIF credentials",
		"pod", pod.Name,
		"namespace", pod.Namespace,
		"serviceAccount", sa.Name)

	return nil
}

// getServiceAccount retrieves the ServiceAccount for the pod
func (p *WIFCredentialsProvider) getServiceAccount(ctx context.Context, pod *corev1.Pod, namespace string) (*corev1.ServiceAccount, error) {
	sa := &corev1.ServiceAccount{}
	saName := pod.Spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}

	reader := p.cache
	if reader == nil {
		reader = p.client
	}

	err := reader.Get(ctx, types.NamespacedName{
		Name:      saName,
		Namespace: namespace,
	}, sa)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("ServiceAccount %s/%s not found: %w", namespace, saName, err)
		}
		return nil, fmt.Errorf("failed to lookup ServiceAccount %s/%s: %w", namespace, saName, err)
	}

	return sa, nil
}

// extractWIFConfig extracts WIF configuration from ServiceAccount annotations
func (p *WIFCredentialsProvider) extractWIFConfig(sa *corev1.ServiceAccount) *WIFConfig {
	config := &WIFConfig{
		UseDirectIdentity: true, // Default to direct identity
	}

	if sa.Annotations == nil {
		config.CredentialsConfigMap = fmt.Sprintf("wif-credentials-direct-%s", sa.Namespace)
		return config
	}

	// Check for service account impersonation
	if gcpSA, exists := sa.Annotations["iam.gke.io/gcp-service-account"]; exists && gcpSA != "" {
		config.UseDirectIdentity = false
		config.GoogleServiceAccount = gcpSA
		config.CredentialsConfigMap = fmt.Sprintf("wif-credentials-impersonate-%s", sa.Name)
	} else {
		config.CredentialsConfigMap = fmt.Sprintf("wif-credentials-direct-%s", sa.Namespace)
	}

	return config
}

// ensureConfigMaps ensures the required ConfigMaps exist for WIF
func (p *WIFCredentialsProvider) ensureConfigMaps(ctx context.Context, namespace string, wifConfig *WIFConfig, pod *corev1.Pod, sa *corev1.ServiceAccount) error {
	configMapName := wifConfig.CredentialsConfigMap

	// Check if ConfigMap already exists
	existingCM := &corev1.ConfigMap{}
	err := p.client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: namespace,
	}, existingCM)

	if err == nil {
		// ConfigMap already exists
		return nil
	}

	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check ConfigMap existence: %w", err)
	}

	// Create new ConfigMap
	credentialsData := p.generateCredentialsConfig(wifConfig)

	cmMeta := metav1.ObjectMeta{
		Name:      configMapName,
		Namespace: namespace,
		Labels: map[string]string{
			"app.kubernetes.io/name":       "k8s-wif-webhook",
			"app.kubernetes.io/component":  "workload-identity",
			"app.kubernetes.io/managed-by": "k8s-wif-webhook",
			"workload-identity.io/type":    p.getConfigMapType(wifConfig),
		},
	}

	// For impersonation ConfigMaps, set ServiceAccount as owner
	if !wifConfig.UseDirectIdentity {
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

	err = p.client.Create(ctx, newCM)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	wifCredentialsLog.Info("Created WIF ConfigMap on-demand",
		"configmap", configMapName,
		"namespace", namespace)

	return nil
}

// injectWIFConfig injects WIF volumes and environment variables into the pod
func (p *WIFCredentialsProvider) injectWIFConfig(pod *corev1.Pod, wifConfig *WIFConfig) error {
	// Parse target containers
	targetContainers := p.parseTargetContainerNames(pod)

	// Add projected token volume
	if err := p.injectTokenVolume(pod); err != nil {
		return fmt.Errorf("failed to inject token volume: %w", err)
	}

	// Add credentials config volume
	if err := p.injectCredentialsVolume(pod, wifConfig); err != nil {
		return fmt.Errorf("failed to inject credentials volume: %w", err)
	}

	// Inject into containers
	if err := p.injectIntoContainers(pod, targetContainers); err != nil {
		return fmt.Errorf("failed to inject into containers: %w", err)
	}

	return nil
}

// injectTokenVolume adds the projected token volume
func (p *WIFCredentialsProvider) injectTokenVolume(pod *corev1.Pod) error {
	if p.volumeManager.VolumeExists(pod, "token") {
		return nil // Volume already exists
	}

	providerPath := p.wifConfig.WorkloadIdentityProvider
	audience := "//iam.googleapis.com/" + providerPath
	expirationSeconds := int64(3600)

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

	return p.volumeManager.AddVolume(pod, tokenVolume)
}

// injectCredentialsVolume adds the credentials config volume
func (p *WIFCredentialsProvider) injectCredentialsVolume(pod *corev1.Pod, wifConfig *WIFConfig) error {
	credentialsVolumeName := "workload-identity-credential-configuration"

	if p.volumeManager.VolumeExists(pod, credentialsVolumeName) {
		return nil // Volume already exists
	}

	credentialsVolume := corev1.Volume{
		Name: credentialsVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: wifConfig.CredentialsConfigMap,
				},
			},
		},
	}

	return p.volumeManager.AddVolume(pod, credentialsVolume)
}

// injectIntoContainers injects volume mounts and environment variables into containers
func (p *WIFCredentialsProvider) injectIntoContainers(pod *corev1.Pod, targetContainers []string) error {
	tokenMountPath := "/var/run/service-account"
	credentialsMountPath := "/etc/workload-identity"

	// Inject into init containers
	for i := range pod.Spec.InitContainers {
		if err := p.injectIntoContainer(&pod.Spec.InitContainers[i], targetContainers, tokenMountPath, credentialsMountPath); err != nil {
			return fmt.Errorf("failed to inject into init container %s: %w", pod.Spec.InitContainers[i].Name, err)
		}
	}

	// Inject into regular containers
	for i := range pod.Spec.Containers {
		if err := p.injectIntoContainer(&pod.Spec.Containers[i], targetContainers, tokenMountPath, credentialsMountPath); err != nil {
			return fmt.Errorf("failed to inject into container %s: %w", pod.Spec.Containers[i].Name, err)
		}
	}

	return nil
}

// injectIntoContainer injects into a single container
func (p *WIFCredentialsProvider) injectIntoContainer(container *corev1.Container, targetContainers []string, tokenMountPath, credentialsMountPath string) error {
	if !p.shouldInjectIntoContainer(container.Name, targetContainers) {
		return nil
	}

	// Add token volume mount
	if !p.volumeManager.VolumeMountExists(container, "token") && !p.volumeManager.MountPathExists(container, tokenMountPath) {
		tokenMount := corev1.VolumeMount{
			Name:      "token",
			MountPath: tokenMountPath,
			ReadOnly:  true,
		}
		if err := p.volumeManager.AddVolumeMount(container, tokenMount); err != nil {
			return err
		}
	}

	// Add credentials volume mount
	credentialsVolumeName := "workload-identity-credential-configuration"
	if !p.volumeManager.VolumeMountExists(container, credentialsVolumeName) && !p.volumeManager.MountPathExists(container, credentialsMountPath) {
		credentialsMount := corev1.VolumeMount{
			Name:      credentialsVolumeName,
			MountPath: credentialsMountPath,
			ReadOnly:  true,
		}
		if err := p.volumeManager.AddVolumeMount(container, credentialsMount); err != nil {
			return err
		}
	}

	// Add GOOGLE_APPLICATION_CREDENTIALS environment variable
	if !p.envVarExists(container, "GOOGLE_APPLICATION_CREDENTIALS") {
		credentialsPath := fmt.Sprintf("%s/credentials.json", credentialsMountPath)
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "GOOGLE_APPLICATION_CREDENTIALS",
			Value: credentialsPath,
		})
	}

	return nil
}

// generateCredentialsConfig generates the WIF credentials configuration JSON
func (p *WIFCredentialsProvider) generateCredentialsConfig(wifConfig *WIFConfig) string {
	providerPath := p.wifConfig.WorkloadIdentityProvider
	audience := "//iam.googleapis.com/" + providerPath

	if wifConfig.UseDirectIdentity {
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
			"service_account_impersonation_url": fmt.Sprintf("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:generateAccessToken", wifConfig.GoogleServiceAccount),
		}
		data, _ := json.MarshalIndent(credConfig, "", "  ")
		return string(data)
	}
}

// Helper methods (extracted for DRY principle)
func (p *WIFCredentialsProvider) parseTargetContainerNames(pod *corev1.Pod) []string {
	if pod.Annotations == nil {
		return nil
	}

	containerNamesAnnotation, exists := pod.Annotations["workload-identity.io/inject-container-names"]
	if !exists || containerNamesAnnotation == "" {
		return nil
	}

	var containerNames []string
	for _, name := range strings.Split(containerNamesAnnotation, ",") {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			containerNames = append(containerNames, trimmed)
		}
	}

	if len(containerNames) == 0 {
		return []string{}
	}

	return containerNames
}

func (p *WIFCredentialsProvider) shouldInjectIntoContainer(containerName string, targetContainers []string) bool {
	if targetContainers == nil {
		return true
	}

	for _, target := range targetContainers {
		if target == containerName {
			return true
		}
	}

	return false
}

func (p *WIFCredentialsProvider) envVarExists(container *corev1.Container, envName string) bool {
	for _, env := range container.Env {
		if env.Name == envName {
			return true
		}
	}
	return false
}

func (p *WIFCredentialsProvider) getConfigMapType(wifConfig *WIFConfig) string {
	if wifConfig.UseDirectIdentity {
		return "direct"
	}
	return "impersonation"
}
