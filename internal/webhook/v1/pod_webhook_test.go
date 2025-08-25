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

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInjectWorkloadIdentityConfig(t *testing.T) {
	// Set required environment variables for testing
	require.NoError(t, os.Setenv("WORKLOAD_IDENTITY_PROVIDER", "projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider"))

	config := &WIFConfig{}

	t.Run("should inject default configuration with 3600 second expiration", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Labels: map[string]string{
					"workload-identity.io/inject": "true",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Find the projected token volume
		var tokenVolume *corev1.Volume
		for i := range pod.Spec.Volumes {
			if pod.Spec.Volumes[i].Name == "token" {
				tokenVolume = &pod.Spec.Volumes[i]
				break
			}
		}

		require.NotNil(t, tokenVolume, "Expected to find token volume")
		require.NotNil(t, tokenVolume.Projected, "Expected projected volume source")
		require.Len(t, tokenVolume.Projected.Sources, 1, "Expected exactly one projection source")

		serviceAccountToken := tokenVolume.Projected.Sources[0].ServiceAccountToken
		require.NotNil(t, serviceAccountToken, "Expected service account token projection")
		require.NotNil(t, serviceAccountToken.ExpirationSeconds, "Expected expiration seconds to be set")
		assert.Equal(t, int64(3600), *serviceAccountToken.ExpirationSeconds, "Expected default 3600 seconds")
	})

	t.Run("should set the correct audience for the token", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Labels: map[string]string{
					"workload-identity.io/inject": "true",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Find the projected token volume
		var tokenVolume *corev1.Volume
		for i := range pod.Spec.Volumes {
			if pod.Spec.Volumes[i].Name == "token" {
				tokenVolume = &pod.Spec.Volumes[i]
				break
			}
		}

		require.NotNil(t, tokenVolume, "Expected to find token volume")
		serviceAccountToken := tokenVolume.Projected.Sources[0].ServiceAccountToken
		require.NotNil(t, serviceAccountToken, "Expected service account token projection")

		expectedAudience := "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider"
		assert.Equal(t, expectedAudience, serviceAccountToken.Audience, "Expected correct audience")
	})

	t.Run("should add volume mounts to first container", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Labels: map[string]string{
					"workload-identity.io/inject": "true",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 2, "Expected two volume mounts")

		tokenMount := pod.Spec.Containers[0].VolumeMounts[0]
		assert.Equal(t, "token", tokenMount.Name, "Expected correct token volume mount name")
		assert.Equal(t, "/var/run/service-account", tokenMount.MountPath, "Expected correct token mount path")
		assert.True(t, tokenMount.ReadOnly, "Expected token volume mount to be read-only")

		credentialsMount := pod.Spec.Containers[0].VolumeMounts[1]
		assert.Equal(t, "workload-identity-credential-configuration", credentialsMount.Name, "Expected correct credentials volume mount name")
		assert.Equal(t, "/etc/workload-identity", credentialsMount.MountPath, "Expected correct credentials mount path")
		assert.True(t, credentialsMount.ReadOnly, "Expected credentials volume mount to be read-only")
	})

	t.Run("should add environment variable to first container", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Labels: map[string]string{
					"workload-identity.io/inject": "true",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		assert.Len(t, pod.Spec.Containers[0].Env, 1, "Expected one environment variable")
		envVar := pod.Spec.Containers[0].Env[0]
		assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", envVar.Name, "Expected correct env var name")
		assert.Equal(t, "/etc/workload-identity/credentials.json", envVar.Value, "Expected correct env var value")
	})

	t.Run("should handle existing volumes without creating duplicates", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "existing-mount",
								MountPath: "/existing",
								ReadOnly:  true,
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "EXISTING_VAR",
								Value: "existing_value",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "existing-volume",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Should have original + 2 new volumes (no duplicates)
		assert.Len(t, pod.Spec.Volumes, 3, "Expected existing volume + 2 WIF volumes")

		// Check volumes exist and are correct
		volumeNames := make([]string, len(pod.Spec.Volumes))
		for i, v := range pod.Spec.Volumes {
			volumeNames[i] = v.Name
		}
		assert.Contains(t, volumeNames, "existing-volume")
		assert.Contains(t, volumeNames, "token")
		assert.Contains(t, volumeNames, "workload-identity-credential-configuration")

		// Should have original + 2 new volume mounts (no duplicates)
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 3, "Expected existing mount + 2 WIF mounts")

		// Should have original + 1 new env var (no duplicates)
		assert.Len(t, pod.Spec.Containers[0].Env, 2, "Expected existing env + 1 WIF env")

		// Check env vars exist and are correct
		envNames := make([]string, len(pod.Spec.Containers[0].Env))
		for i, e := range pod.Spec.Containers[0].Env {
			envNames[i] = e.Name
		}
		assert.Contains(t, envNames, "EXISTING_VAR")
		assert.Contains(t, envNames, "GOOGLE_APPLICATION_CREDENTIALS")
	})

	t.Run("should skip injection when conflicting volume names exist", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "token", // Conflicts with WIF volume name
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
			},
		}

		// This should cause duplicate volume names after injection
		injectWorkloadIdentityConfig(nil, pod, config)

		// Bug: Currently creates duplicate volume names
		volumeNames := make([]string, len(pod.Spec.Volumes))
		for i, v := range pod.Spec.Volumes {
			volumeNames[i] = v.Name
		}

		// Count "token" volumes - should be 1 but will be 2 due to bug
		tokenCount := 0
		for _, name := range volumeNames {
			if name == "token" {
				tokenCount++
			}
		}

		// This should now pass - webhook skips injection for conflicting volume names
		assert.Equal(t, 1, tokenCount, "Should only have one 'token' volume - injection was skipped")
	})

	t.Run("should skip injection when conflicting mount paths exist", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "existing-token",
								MountPath: "/var/run/service-account", // Conflicts with WIF mount path
								ReadOnly:  true,
							},
						},
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Count mounts at same path - should be 1 but will be 2 due to bug
		pathCount := 0
		for _, mount := range pod.Spec.Containers[0].VolumeMounts {
			if mount.MountPath == "/var/run/service-account" {
				pathCount++
			}
		}

		// This should now pass - webhook skips injection for conflicting mount paths
		assert.Equal(t, 1, pathCount, "Should only have one mount at '/var/run/service-account' - injection was skipped")
	})

	t.Run("should skip injection when GOOGLE_APPLICATION_CREDENTIALS already exists", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
						Env: []corev1.EnvVar{
							{
								Name:  "GOOGLE_APPLICATION_CREDENTIALS",
								Value: "/existing/path/to/creds.json",
							},
						},
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Count GOOGLE_APPLICATION_CREDENTIALS env vars - should be 1 but will be 2 due to bug
		credsCount := 0
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
				credsCount++
			}
		}

		// This should now pass - webhook skips injection for existing env vars
		assert.Equal(t, 1, credsCount, "Should only have one GOOGLE_APPLICATION_CREDENTIALS env var - injection was skipped")
	})

	t.Run("should inject WIF into all containers when no annotation specified", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
					{
						Name:  "another-container",
						Image: "redis",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Volumes should be added once at pod level
		assert.Len(t, pod.Spec.Volumes, 2, "Expected two volumes (token and credentials)")

		// All containers should get volume mounts and env vars
		for i, container := range pod.Spec.Containers {
			assert.Len(t, container.VolumeMounts, 2, "Container %d should have two volume mounts", i)
			assert.Len(t, container.Env, 1, "Container %d should have one env var", i)
			assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", container.Env[0].Name, "Container %d should have correct env var", i)
		}
	})

	t.Run("should inject WIF only into specified containers", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": "app-container,another-container",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
					{
						Name:  "another-container",
						Image: "redis",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Volumes should be added once at pod level
		assert.Len(t, pod.Spec.Volumes, 2, "Expected two volumes (token and credentials)")

		// Check first container (should be injected)
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 2, "First container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 1, "First container should have env var")
		assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", pod.Spec.Containers[0].Env[0].Name)

		// Check second container (should not be injected)
		assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 0, "Second container should have no volume mounts")
		assert.Len(t, pod.Spec.Containers[1].Env, 0, "Second container should have no env vars")

		// Check third container (should be injected)
		assert.Len(t, pod.Spec.Containers[2].VolumeMounts, 2, "Third container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[2].Env, 1, "Third container should have env var")
		assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", pod.Spec.Containers[2].Env[0].Name)
	})

	t.Run("should handle whitespace in container names annotation", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": " app-container , sidecar-container , ",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
					{
						Name:  "ignored-container",
						Image: "redis",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Check first container (should be injected - trimmed whitespace)
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 2, "First container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 1, "First container should have env var")

		// Check second container (should be injected - trimmed whitespace)
		assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 2, "Second container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[1].Env, 1, "Second container should have env var")

		// Check third container (should not be injected)
		assert.Len(t, pod.Spec.Containers[2].VolumeMounts, 0, "Third container should have no volume mounts")
		assert.Len(t, pod.Spec.Containers[2].Env, 0, "Third container should have no env vars")
	})

	t.Run("should handle empty container names annotation", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": "",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Empty annotation should result in injection into all containers
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 2, "First container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 1, "First container should have env var")
		assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 2, "Second container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[1].Env, 1, "Second container should have env var")
	})

	t.Run("should handle non-existent container names in annotation", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": "app-container,non-existent-container",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Only the existing container should be injected
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 2, "First container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 1, "First container should have env var")
		assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 0, "Second container should have no volume mounts")
		assert.Len(t, pod.Spec.Containers[1].Env, 0, "Second container should have no env vars")
	})

	t.Run("should inject only single container when specified", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-container-pod",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": "sidecar-container",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Volumes should still be at pod level
		assert.Len(t, pod.Spec.Volumes, 2, "Expected two volumes (token and credentials)")

		// Only second container should be injected
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 0, "First container should have no volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 0, "First container should have no env vars")
		assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 2, "Second container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[1].Env, 1, "Second container should have env var")
	})

	t.Run("should inject WIF into init containers when specified", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-init-containers",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": "init-setup,app-container",
				},
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name:    "init-setup",
						Image:   "alpine",
						Command: []string{"sh", "-c", "echo setting up"},
					},
					{
						Name:    "init-db-migrate",
						Image:   "migrate/migrate",
						Command: []string{"migrate", "up"},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "sidecar-container",
						Image: "busybox",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Volumes should be added once at pod level
		assert.Len(t, pod.Spec.Volumes, 2, "Expected two volumes (token and credentials)")

		// Check init containers - only first should be injected
		assert.Len(t, pod.Spec.InitContainers[0].VolumeMounts, 2, "First init container should have volume mounts")
		assert.Len(t, pod.Spec.InitContainers[0].Env, 1, "First init container should have env var")
		assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", pod.Spec.InitContainers[0].Env[0].Name)

		assert.Len(t, pod.Spec.InitContainers[1].VolumeMounts, 0, "Second init container should have no volume mounts")
		assert.Len(t, pod.Spec.InitContainers[1].Env, 0, "Second init container should have no env vars")

		// Check regular containers - only first should be injected
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 2, "First container should have volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 1, "First container should have env var")
		assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", pod.Spec.Containers[0].Env[0].Name)

		assert.Len(t, pod.Spec.Containers[1].VolumeMounts, 0, "Second container should have no volume mounts")
		assert.Len(t, pod.Spec.Containers[1].Env, 0, "Second container should have no env vars")
	})

	t.Run("should inject WIF into all containers including init containers when no annotation", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-all-containers",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name:    "init-secrets",
						Image:   "vault:1.12",
						Command: []string{"vault", "kv", "get"},
					},
					{
						Name:    "init-config",
						Image:   "busybox",
						Command: []string{"sh", "-c", "echo configuring"},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
					{
						Name:  "monitoring-container",
						Image: "prometheus/node-exporter",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Volumes should be added once at pod level
		assert.Len(t, pod.Spec.Volumes, 2, "Expected two volumes (token and credentials)")

		// All init containers should get volume mounts and env vars
		for i, container := range pod.Spec.InitContainers {
			assert.Len(t, container.VolumeMounts, 2, "Init container %d should have two volume mounts", i)
			assert.Len(t, container.Env, 1, "Init container %d should have one env var", i)
			assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", container.Env[0].Name, "Init container %d should have correct env var", i)
		}

		// All regular containers should get volume mounts and env vars
		for i, container := range pod.Spec.Containers {
			assert.Len(t, container.VolumeMounts, 2, "Container %d should have two volume mounts", i)
			assert.Len(t, container.Env, 1, "Container %d should have one env var", i)
			assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", container.Env[0].Name, "Container %d should have correct env var", i)
		}
	})

	t.Run("should inject WIF only into specified init containers", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-targeted-init-containers",
				Namespace: "default",
				Annotations: map[string]string{
					"workload-identity.io/inject-container-names": "init-secrets",
				},
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name:    "init-secrets",
						Image:   "vault:1.12",
						Command: []string{"vault", "kv", "get"},
					},
					{
						Name:    "init-local-setup",
						Image:   "busybox",
						Command: []string{"sh", "-c", "echo local setup"},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "app-container",
						Image: "nginx",
					},
				},
			},
		}

		injectWorkloadIdentityConfig(nil, pod, config)

		// Volumes should be added once at pod level
		assert.Len(t, pod.Spec.Volumes, 2, "Expected two volumes (token and credentials)")

		// Only first init container should be injected
		assert.Len(t, pod.Spec.InitContainers[0].VolumeMounts, 2, "First init container should have volume mounts")
		assert.Len(t, pod.Spec.InitContainers[0].Env, 1, "First init container should have env var")

		assert.Len(t, pod.Spec.InitContainers[1].VolumeMounts, 0, "Second init container should have no volume mounts")
		assert.Len(t, pod.Spec.InitContainers[1].Env, 0, "Second init container should have no env vars")

		// Regular container should not be injected
		assert.Len(t, pod.Spec.Containers[0].VolumeMounts, 0, "App container should have no volume mounts")
		assert.Len(t, pod.Spec.Containers[0].Env, 0, "App container should have no env vars")
	})

}

func TestParseTargetContainerNames(t *testing.T) {
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
			name:        "empty annotations",
			annotations: map[string]string{},
			expected:    nil,
		},
		{
			name: "annotation not present",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			expected: nil,
		},
		{
			name: "empty annotation value",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "",
			},
			expected: nil,
		},
		{
			name: "single container",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "app-container",
			},
			expected: []string{"app-container"},
		},
		{
			name: "multiple containers",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "app-container,sidecar-container,another-container",
			},
			expected: []string{"app-container", "sidecar-container", "another-container"},
		},
		{
			name: "containers with whitespace",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": " app-container , sidecar-container , another-container ",
			},
			expected: []string{"app-container", "sidecar-container", "another-container"},
		},
		{
			name: "containers with empty entries",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": "app-container,,sidecar-container,",
			},
			expected: []string{"app-container", "sidecar-container"},
		},
		{
			name: "only commas and whitespace",
			annotations: map[string]string{
				"workload-identity.io/inject-container-names": " , , ",
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
			result := parseTargetContainerNames(pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldInjectIntoContainer(t *testing.T) {
	tests := []struct {
		name             string
		containerName    string
		targetContainers []string
		expected         bool
	}{
		{
			name:             "no target containers specified - inject all",
			containerName:    "app-container",
			targetContainers: nil,
			expected:         true,
		},
		{
			name:             "container in target list",
			containerName:    "app-container",
			targetContainers: []string{"app-container", "sidecar-container"},
			expected:         true,
		},
		{
			name:             "container not in target list",
			containerName:    "ignored-container",
			targetContainers: []string{"app-container", "sidecar-container"},
			expected:         false,
		},
		{
			name:             "empty target list - inject none",
			containerName:    "app-container",
			targetContainers: []string{},
			expected:         false,
		},
		{
			name:             "single target container - match",
			containerName:    "app-container",
			targetContainers: []string{"app-container"},
			expected:         true,
		},
		{
			name:             "single target container - no match",
			containerName:    "sidecar-container",
			targetContainers: []string{"app-container"},
			expected:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldInjectIntoContainer(tt.containerName, tt.targetContainers)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPotentialPodName(t *testing.T) {
	tests := []struct {
		name     string
		metadata metav1.ObjectMeta
		expected string
	}{
		{
			name: "pod with explicit name",
			metadata: metav1.ObjectMeta{
				Name:      "my-pod",
				Namespace: "default",
			},
			expected: "my-pod",
		},
		{
			name: "pod with generateName (typical Deployment pattern)",
			metadata: metav1.ObjectMeta{
				GenerateName: "web-deployment-abc123-",
				Namespace:    "default",
			},
			expected: "web-deployment-abc123-***** (actual name not yet known)",
		},
		{
			name: "pod with both name and generateName (name takes precedence)",
			metadata: metav1.ObjectMeta{
				Name:         "explicit-name",
				GenerateName: "should-be-ignored-",
				Namespace:    "default",
			},
			expected: "explicit-name",
		},
		{
			name: "pod with neither name nor generateName",
			metadata: metav1.ObjectMeta{
				Namespace: "default",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := potentialPodName(tt.metadata)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestPodWebhookIntegration(t *testing.T) {
	// Set required environment variables for testing
	require.NoError(t, os.Setenv("PROJECT_NUMBER", "123456789"))
	require.NoError(t, os.Setenv("POOL_ID", "test-pool"))
	require.NoError(t, os.Setenv("PROVIDER_ID", "test-provider"))
	defer func() {
		require.NoError(t, os.Unsetenv("PROJECT_NUMBER"))
		require.NoError(t, os.Unsetenv("POOL_ID"))
		require.NoError(t, os.Unsetenv("PROVIDER_ID"))
	}()

	testSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "default",
		},
	}

	runtimeScheme := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(runtimeScheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(runtimeScheme).
		WithObjects(testSA).
		Build()

	webhook := &PodCustomDefaulter{
		Client: fakeClient,
		Cache:  fakeClient,
	}

	ctx := context.Background()

	tests := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "pod with explicit name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "explicit-pod-name",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "default",
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx"},
					},
				},
			},
		},
		{
			name: "pod with generateName (Deployment pattern)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "web-deployment-abc123-",
					Namespace:    "default",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "default",
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx"},
					},
				},
			},
		},
		{
			name: "pod with neither name nor generateName",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "default",
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that webhook processes without error
			err := webhook.Default(ctx, tt.pod)
			require.NoError(t, err)

			// Verify WIF injection occurred
			assert.True(t, hasWorkloadIdentityConfig(tt.pod), "WIF configuration should be injected")

			// Verify volumes were added
			assert.Len(t, tt.pod.Spec.Volumes, 2, "Should have token and credentials volumes")

			// Verify container modifications
			require.Len(t, tt.pod.Spec.Containers, 1, "Should still have one container")
			container := tt.pod.Spec.Containers[0]

			// Check volume mounts
			assert.Len(t, container.VolumeMounts, 2, "Should have WIF volume mounts")

			// Check environment variable
			assert.Len(t, container.Env, 1, "Should have GOOGLE_APPLICATION_CREDENTIALS env var")
			assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", container.Env[0].Name)
		})
	}
}

// BenchmarkWebhookDefault measures webhook performance
func BenchmarkWebhookDefault(b *testing.B) {
	// Set required environment variables for testing
	require.NoError(b, os.Setenv("PROJECT_NUMBER", "123456789"))
	require.NoError(b, os.Setenv("POOL_ID", "test-pool"))
	require.NoError(b, os.Setenv("PROVIDER_ID", "test-provider"))
	defer func() {
		require.NoError(b, os.Unsetenv("PROJECT_NUMBER"))
		require.NoError(b, os.Unsetenv("POOL_ID"))
		require.NoError(b, os.Unsetenv("PROVIDER_ID"))
	}()

	// Create test objects
	testSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "default",
		},
	}

	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "default",
			Containers: []corev1.Container{
				{
					Name:  "test-container",
					Image: "nginx",
				},
			},
		},
	}

	// Create fake client with test objects
	runtimeScheme := runtime.NewScheme()
	require.NoError(b, scheme.AddToScheme(runtimeScheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(runtimeScheme).
		WithObjects(testSA).
		Build()

	webhook := &PodCustomDefaulter{
		Client: fakeClient,
		Cache:  fakeClient, // Use same client as cache for testing
	}

	ctx := context.Background()

	// Reset timer to exclude setup time
	b.ResetTimer()

	// Run benchmark
	for i := 0; i < b.N; i++ {
		// Create a fresh copy of the pod for each iteration
		pod := testPod.DeepCopy()

		err := webhook.Default(ctx, pod)
		if err != nil {
			b.Fatalf("webhook failed: %v", err)
		}
	}
}

// BenchmarkWebhookDefaultWithConfigMapCreation measures webhook performance including ConfigMap creation
func BenchmarkWebhookDefaultWithConfigMapCreation(b *testing.B) {
	// Set required environment variables for testing
	require.NoError(b, os.Setenv("PROJECT_NUMBER", "123456789"))
	require.NoError(b, os.Setenv("POOL_ID", "test-pool"))
	require.NoError(b, os.Setenv("PROVIDER_ID", "test-provider"))
	defer func() {
		require.NoError(b, os.Unsetenv("PROJECT_NUMBER"))
		require.NoError(b, os.Unsetenv("POOL_ID"))
		require.NoError(b, os.Unsetenv("PROVIDER_ID"))
	}()

	// Create test objects
	testSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "default",
		},
	}

	// Create fake client with test objects
	runtimeScheme := runtime.NewScheme()
	require.NoError(b, scheme.AddToScheme(runtimeScheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(runtimeScheme).
		WithObjects(testSA).
		Build()

	webhook := &PodCustomDefaulter{
		Client: fakeClient,
		Cache:  fakeClient,
	}

	ctx := context.Background()

	// Reset timer to exclude setup time
	b.ResetTimer()

	// Run benchmark - each iteration creates a pod in a new namespace
	// to force ConfigMap creation
	for i := 0; i < b.N; i++ {
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("bench-ns-%d", i),
			},
		}

		testPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: namespace.Name,
			},
			Spec: corev1.PodSpec{
				ServiceAccountName: "default",
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "nginx",
					},
				},
			},
		}

		// Create namespace and SA for this iteration
		nsScoped := testSA.DeepCopy()
		nsScoped.Namespace = namespace.Name

		require.NoError(b, fakeClient.Create(ctx, namespace))
		require.NoError(b, fakeClient.Create(ctx, nsScoped))

		err := webhook.Default(ctx, testPod)
		if err != nil {
			b.Fatalf("webhook failed: %v", err)
		}
	}
}
