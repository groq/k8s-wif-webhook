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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestClient() client.Client {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestKubernetesSecretSyncer_CreateOrUpdateInNamespace_Create(t *testing.T) {
	mockClient := newTestClient()
	syncer := NewKubernetesSecretSyncer(mockClient, "default", "test-secret", "creds.json")

	credentialsData := []byte(`{"type": "authorized_user"}`)

	err := syncer.CreateOrUpdateInNamespace(context.Background(), "default", credentialsData)
	require.NoError(t, err)

	// Verify secret was created
	secret := &corev1.Secret{}
	err = mockClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-secret",
		Namespace: "default",
	}, secret)
	require.NoError(t, err)

	assert.Equal(t, "default", secret.Namespace)
	assert.Equal(t, credentialsData, secret.Data["creds.json"])
	assert.Equal(t, "k8s-wif-webhook", secret.Labels["app.kubernetes.io/name"])
	assert.Equal(t, "development-credentials", secret.Labels["app.kubernetes.io/component"])
	assert.Equal(t, "k8s-wif-webhook", secret.Labels["app.kubernetes.io/managed-by"])
}

func TestKubernetesSecretSyncer_CreateOrUpdateInNamespace_Update(t *testing.T) {
	mockClient := newTestClient()
	syncer := NewKubernetesSecretSyncer(mockClient, "default", "test-secret", "creds.json")

	// Pre-create secret with old data
	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"creds.json": []byte(`{"type": "old"}`),
		},
	}
	err := mockClient.Create(context.Background(), existingSecret)
	require.NoError(t, err)

	// Update with new data
	newCredentialsData := []byte(`{"type": "authorized_user"}`)
	err = syncer.CreateOrUpdateInNamespace(context.Background(), "default", newCredentialsData)
	require.NoError(t, err)

	// Verify secret was updated
	secret := &corev1.Secret{}
	err = mockClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-secret",
		Namespace: "default",
	}, secret)
	require.NoError(t, err)

	assert.Equal(t, "default", secret.Namespace)
	assert.Equal(t, newCredentialsData, secret.Data["creds.json"])
	assert.Equal(t, "k8s-wif-webhook", secret.Labels["app.kubernetes.io/name"])
	assert.Equal(t, "development-credentials", secret.Labels["app.kubernetes.io/component"])
	assert.Equal(t, "k8s-wif-webhook", secret.Labels["app.kubernetes.io/managed-by"])
}

func TestKubernetesSecretSyncer_CreateOrUpdateInNamespace_DifferentNamespace(t *testing.T) {
	mockClient := newTestClient()
	syncer := NewKubernetesSecretSyncer(mockClient, "default", "test-secret", "creds.json")

	credentialsData := []byte(`{"type": "authorized_user"}`)

	// Create in a different namespace than the syncer's default
	err := syncer.CreateOrUpdateInNamespace(context.Background(), "custom-namespace", credentialsData)
	require.NoError(t, err)

	// Verify secret was created in the specified namespace
	secret := &corev1.Secret{}
	err = mockClient.Get(context.Background(), client.ObjectKey{
		Name:      "test-secret",
		Namespace: "custom-namespace",
	}, secret)
	require.NoError(t, err)

	assert.Equal(t, "custom-namespace", secret.Namespace)
	assert.Equal(t, credentialsData, secret.Data["creds.json"])
}
