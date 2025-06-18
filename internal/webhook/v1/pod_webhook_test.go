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
	os.Setenv("WORKLOAD_IDENTITY_PROVIDER", "projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider")

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

		injectWorkloadIdentityConfig(pod, config)

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

		injectWorkloadIdentityConfig(pod, config)

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

		injectWorkloadIdentityConfig(pod, config)

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

		injectWorkloadIdentityConfig(pod, config)

		assert.Len(t, pod.Spec.Containers[0].Env, 1, "Expected one environment variable")
		envVar := pod.Spec.Containers[0].Env[0]
		assert.Equal(t, "GOOGLE_APPLICATION_CREDENTIALS", envVar.Name, "Expected correct env var name")
		assert.Equal(t, "/etc/workload-identity/credentials.json", envVar.Value, "Expected correct env var value")
	})

}

// BenchmarkWebhookDefault measures webhook performance
func BenchmarkWebhookDefault(b *testing.B) {
	// Set required environment variables for testing
	os.Setenv("PROJECT_NUMBER", "123456789")
	os.Setenv("POOL_ID", "test-pool")
	os.Setenv("PROVIDER_ID", "test-provider")
	defer func() {
		os.Unsetenv("PROJECT_NUMBER")
		os.Unsetenv("POOL_ID")
		os.Unsetenv("PROVIDER_ID")
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
	os.Setenv("PROJECT_NUMBER", "123456789")
	os.Setenv("POOL_ID", "test-pool")
	os.Setenv("PROVIDER_ID", "test-provider")
	defer func() {
		os.Unsetenv("PROJECT_NUMBER")
		os.Unsetenv("POOL_ID")
		os.Unsetenv("PROVIDER_ID")
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
