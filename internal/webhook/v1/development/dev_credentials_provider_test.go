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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/groq/k8s-wif-webhook/internal/webhook/v1/volume"
)

// mockClient implements a simple mock for the client
type mockClient struct {
	client.Client
	objects []client.Object
}

func newMockClient(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		Build()
}

func TestDevCredentialsProvider_GetCredentialType(t *testing.T) {
	provider := NewDevCredentialsProvider(nil, nil, "")
	assert.Equal(t, "development", provider.GetCredentialType())
}

func TestDevCredentialsProvider_RequiresSetup(t *testing.T) {
	provider := NewDevCredentialsProvider(nil, nil, "")
	assert.True(t, provider.RequiresSetup())
}

func TestDevCredentialsProvider_Setup(t *testing.T) {
	// Create a temporary credentials file
	tempDir := t.TempDir()
	credentialsFile := filepath.Join(tempDir, "application_default_credentials.json")

	tests := []struct {
		name        string
		createFile  bool
		expectedErr bool
	}{
		{
			name:        "credentials file exists",
			createFile:  true,
			expectedErr: false,
		},
		{
			name:        "credentials file does not exist",
			createFile:  false,
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Remove file if it exists
			os.Remove(credentialsFile)

			if tt.createFile {
				err := os.WriteFile(credentialsFile, []byte(`{"type": "authorized_user"}`), 0644)
				require.NoError(t, err)
			}

			provider := NewDevCredentialsProvider(nil, nil, credentialsFile)
			err := provider.Setup()

			if tt.expectedErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "development credentials file not found")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDevCredentialsProvider_InjectCredentials(t *testing.T) {
	// Create a temporary credentials file
	tempDir := t.TempDir()
	credentialsFile := filepath.Join(tempDir, "application_default_credentials.json")
	credentialsContent := `{"type": "authorized_user", "client_id": "test"}`
	err := os.WriteFile(credentialsFile, []byte(credentialsContent), 0644)
	require.NoError(t, err)

	// Create mock client
	mockClient := newMockClient()
	volumeManager := volume.NewDefaultVolumeManager()

	provider := NewDevCredentialsProvider(mockClient, volumeManager, credentialsFile)

	// Create test pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
				},
			},
		},
	}

	// Test injection
	err = provider.InjectCredentials(pod, "default")
	require.NoError(t, err)

	// Verify volume was added
	assert.Len(t, pod.Spec.Volumes, 1)
	assert.Equal(t, DevCredentialsVolumeName, pod.Spec.Volumes[0].Name)
	assert.NotNil(t, pod.Spec.Volumes[0].Secret)
	assert.Equal(t, DevCredentialsSecretName, pod.Spec.Volumes[0].Secret.SecretName)

	// Verify volume mount was added to container
	assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, DevCredentialsVolumeName, pod.Spec.Containers[0].VolumeMounts[0].Name)
	assert.Equal(t, DevCredentialsMountPath, pod.Spec.Containers[0].VolumeMounts[0].MountPath)
	assert.True(t, pod.Spec.Containers[0].VolumeMounts[0].ReadOnly)

	// Verify environment variable was added
	found := false
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
			found = true
			expectedPath := DevCredentialsMountPath + "/" + DevCredentialsSecretKey
			assert.Equal(t, expectedPath, env.Value)
			break
		}
	}
	assert.True(t, found, "GOOGLE_APPLICATION_CREDENTIALS environment variable not found")
}

func TestDevCredentialsProvider_InjectCredentials_WithTargetContainers(t *testing.T) {
	// Create a temporary credentials file
	tempDir := t.TempDir()
	credentialsFile := filepath.Join(tempDir, "application_default_credentials.json")
	credentialsContent := `{"type": "authorized_user", "client_id": "test"}`
	err := os.WriteFile(credentialsFile, []byte(credentialsContent), 0644)
	require.NoError(t, err)

	// Create mock client
	mockClient := newMockClient()
	volumeManager := volume.NewDefaultVolumeManager()

	provider := NewDevCredentialsProvider(mockClient, volumeManager, credentialsFile)

	// Create test pod with annotation targeting specific container
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Annotations: map[string]string{
				"workload-identity.io/inject-container-names": "app",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
				},
				{
					Name:  "sidecar",
					Image: "busybox",
				},
			},
		},
	}

	// Test injection
	err = provider.InjectCredentials(pod, "default")
	require.NoError(t, err)

	// Verify volume mount was only added to target container
	assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 1, "app container should have volume mount")
	assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 0, "sidecar container should not have volume mount")

	// Verify environment variable was only added to target container
	appHasEnv := false
	sidecarHasEnv := false

	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
			appHasEnv = true
			break
		}
	}

	for _, env := range pod.Spec.Containers[1].Env {
		if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
			sidecarHasEnv = true
			break
		}
	}

	assert.True(t, appHasEnv, "app container should have GOOGLE_APPLICATION_CREDENTIALS")
	assert.False(t, sidecarHasEnv, "sidecar container should not have GOOGLE_APPLICATION_CREDENTIALS")
}

func TestDevCredentialsProvider_parseTargetContainerNames(t *testing.T) {
	provider := &DevCredentialsProvider{}

	tests := []struct {
		name        string
		annotations map[string]string
		expected    []string
	}{
		{
			name:        "no annotations",
			annotations: nil,
			expected:    nil,
		},
		{
			name:        "no target annotation",
			annotations: map[string]string{"other": "value"},
			expected:    nil,
		},
		{
			name: "single container",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "app",
			},
			expected: []string{"app"},
		},
		{
			name: "multiple containers",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "app,sidecar,worker",
			},
			expected: []string{"app", "sidecar", "worker"},
		},
		{
			name: "containers with whitespace",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": " app , sidecar , worker ",
			},
			expected: []string{"app", "sidecar", "worker"},
		},
		{
			name: "empty annotation",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "",
			},
			expected: nil,
		},
		{
			name: "only whitespace",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "   ",
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tt.annotations,
				},
			}

			result := provider.parseTargetContainerNames(pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDevCredentialsProvider_shouldInjectIntoContainer(t *testing.T) {
	provider := &DevCredentialsProvider{}

	tests := []struct {
		name             string
		containerName    string
		targetContainers []string
		expected         bool
	}{
		{
			name:             "no target containers (inject all)",
			containerName:    "app",
			targetContainers: nil,
			expected:         true,
		},
		{
			name:             "container in target list",
			containerName:    "app",
			targetContainers: []string{"app", "sidecar"},
			expected:         true,
		},
		{
			name:             "container not in target list",
			containerName:    "worker",
			targetContainers: []string{"app", "sidecar"},
			expected:         false,
		},
		{
			name:             "empty target list",
			containerName:    "app",
			targetContainers: []string{},
			expected:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.shouldInjectIntoContainer(tt.containerName, tt.targetContainers)
			assert.Equal(t, tt.expected, result)
		})
	}
}
