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

package development

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/groq/k8s-wif-webhook/internal/webhook/v1/config"
)

var devCredentialsLog = log.Log.WithName("dev-credentials")

const (
	// DevCredentialsSecretName is the default name for development credentials secret
	DevCredentialsSecretName = "gcloud-dev-credentials"
	// DevCredentialsSecretKey is the key in the secret containing the credentials
	DevCredentialsSecretKey = "application_default_credentials.json"
	// DevCredentialsMountPath is where the credentials are mounted in containers
	DevCredentialsMountPath = "/var/secrets/google"
	// DevCredentialsVolumeName is the name of the volume containing dev credentials
	DevCredentialsVolumeName = "gcloud-dev-credentials"
)

// DevCredentialsProvider provides development credentials via Kubernetes secrets
// Following Single Responsibility Principle - only handles development credential injection
type DevCredentialsProvider struct {
	client          client.Client
	volumeManager   config.VolumeManager
	credentialsPath string
}

// NewDevCredentialsProvider creates a new development credentials provider
func NewDevCredentialsProvider(client client.Client, volumeManager config.VolumeManager, credentialsPath string) *DevCredentialsProvider {
	return &DevCredentialsProvider{
		client:          client,
		volumeManager:   volumeManager,
		credentialsPath: credentialsPath,
	}
}

// GetCredentialType implements CredentialsProvider interface
func (p *DevCredentialsProvider) GetCredentialType() string {
	return "development"
}

// RequiresSetup implements CredentialsProvider interface
func (p *DevCredentialsProvider) RequiresSetup() bool {
	return true
}

// Setup implements CredentialsProvider interface
func (p *DevCredentialsProvider) Setup() error {
	// Check if credentials file exists
	if _, err := os.Stat(p.credentialsPath); os.IsNotExist(err) {
		return fmt.Errorf("development credentials file not found at %s. Please run 'gcloud auth application-default login'", p.credentialsPath)
	}

	devCredentialsLog.Info("Development credentials provider setup complete", "path", p.credentialsPath)
	return nil
}

// InjectCredentials implements CredentialsProvider interface
func (p *DevCredentialsProvider) InjectCredentials(pod *corev1.Pod, namespace string) error {
	// Ensure the development credentials secret exists in the pod's namespace
	if err := p.ensureSecretInNamespace(context.Background(), namespace); err != nil {
		return fmt.Errorf("failed to ensure development credentials secret: %w", err)
	}

	// Add volume for development credentials
	if err := p.injectCredentialsVolume(pod); err != nil {
		return fmt.Errorf("failed to inject credentials volume: %w", err)
	}

	// Parse target containers
	targetContainers := p.parseTargetContainerNames(pod)

	// Add volume mounts and environment variables to containers
	if err := p.injectIntoContainers(pod, targetContainers); err != nil {
		return fmt.Errorf("failed to inject into containers: %w", err)
	}

	devCredentialsLog.Info("Injected development credentials",
		"pod", pod.Name,
		"namespace", pod.Namespace)

	return nil
}

// ensureSecretInNamespace ensures the development credentials secret exists in the given namespace
func (p *DevCredentialsProvider) ensureSecretInNamespace(ctx context.Context, namespace string) error {
	// Read current credentials from file
	credentialsData, err := os.ReadFile(p.credentialsPath)
	if err != nil {
		return fmt.Errorf("failed to read credentials file: %w", err)
	}

	// Create syncer for this specific namespace
	syncer := NewKubernetesSecretSyncer(p.client, namespace, DevCredentialsSecretName, DevCredentialsSecretKey)

	// Sync credentials to the namespace
	return syncer.CreateOrUpdateInNamespace(ctx, namespace, credentialsData)
}

// injectCredentialsVolume adds the development credentials volume to the pod
func (p *DevCredentialsProvider) injectCredentialsVolume(pod *corev1.Pod) error {
	if p.volumeManager.VolumeExists(pod, DevCredentialsVolumeName) {
		devCredentialsLog.V(1).Info("Development credentials volume already exists",
			"pod", pod.Name,
			"volume", DevCredentialsVolumeName)
		return nil
	}

	volume := corev1.Volume{
		Name: DevCredentialsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: DevCredentialsSecretName,
			},
		},
	}

	return p.volumeManager.AddVolume(pod, volume)
}

// injectIntoContainers injects volume mounts and environment variables into containers
func (p *DevCredentialsProvider) injectIntoContainers(pod *corev1.Pod, targetContainers []string) error {
	// Inject into init containers
	for i := range pod.Spec.InitContainers {
		if err := p.injectIntoContainer(&pod.Spec.InitContainers[i], targetContainers); err != nil {
			return fmt.Errorf("failed to inject into init container %s: %w", pod.Spec.InitContainers[i].Name, err)
		}
	}

	// Inject into regular containers
	for i := range pod.Spec.Containers {
		if err := p.injectIntoContainer(&pod.Spec.Containers[i], targetContainers); err != nil {
			return fmt.Errorf("failed to inject into container %s: %w", pod.Spec.Containers[i].Name, err)
		}
	}

	return nil
}

// injectIntoContainer injects volume mount and environment variable into a single container
func (p *DevCredentialsProvider) injectIntoContainer(container *corev1.Container, targetContainers []string) error {
	// Check if this container should receive injection
	if !p.shouldInjectIntoContainer(container.Name, targetContainers) {
		return nil
	}

	// Add volume mount
	if !p.volumeManager.VolumeMountExists(container, DevCredentialsVolumeName) {
		if p.volumeManager.MountPathExists(container, DevCredentialsMountPath) {
			devCredentialsLog.Info("Mount path already exists, skipping volume mount",
				"container", container.Name,
				"mountPath", DevCredentialsMountPath)
		} else {
			volumeMount := corev1.VolumeMount{
				Name:      DevCredentialsVolumeName,
				MountPath: DevCredentialsMountPath,
				ReadOnly:  true,
			}
			if err := p.volumeManager.AddVolumeMount(container, volumeMount); err != nil {
				return err
			}
		}
	}

	// Add GOOGLE_APPLICATION_CREDENTIALS environment variable
	if !p.envVarExists(container, "GOOGLE_APPLICATION_CREDENTIALS") {
		credentialsPath := fmt.Sprintf("%s/%s", DevCredentialsMountPath, DevCredentialsSecretKey)
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "GOOGLE_APPLICATION_CREDENTIALS",
			Value: credentialsPath,
		})
	}

	return nil
}

// parseTargetContainerNames parses the target container names from pod annotations
func (p *DevCredentialsProvider) parseTargetContainerNames(pod *corev1.Pod) []string {
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

// shouldInjectIntoContainer determines if development credentials should be injected into the container
func (p *DevCredentialsProvider) shouldInjectIntoContainer(containerName string, targetContainers []string) bool {
	// If no target containers specified, inject into all containers
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

// envVarExists checks if an environment variable exists in the container
func (p *DevCredentialsProvider) envVarExists(container *corev1.Container, envName string) bool {
	for _, env := range container.Env {
		if env.Name == envName {
			return true
		}
	}
	return false
}
