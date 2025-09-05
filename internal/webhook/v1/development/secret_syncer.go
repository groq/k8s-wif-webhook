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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var secretSyncerLog = log.Log.WithName("secret-syncer")

// KubernetesSecretSyncer syncs credentials to Kubernetes secrets
// Following Single Responsibility Principle - only handles secret operations
type KubernetesSecretSyncer struct {
	client     client.Client
	secretName string
	secretKey  string
	namespace  string
}

// NewKubernetesSecretSyncer creates a new Kubernetes secret syncer
func NewKubernetesSecretSyncer(client client.Client, namespace, secretName, secretKey string) *KubernetesSecretSyncer {
	return &KubernetesSecretSyncer{
		client:     client,
		secretName: secretName,
		secretKey:  secretKey,
		namespace:  namespace,
	}
}

// CreateOrUpdateInNamespace creates or updates the development credentials secret in a specific namespace
func (s *KubernetesSecretSyncer) CreateOrUpdateInNamespace(ctx context.Context, namespace string, credentialsData []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.secretName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "k8s-wif-webhook",
				"app.kubernetes.io/component":  "development-credentials",
				"app.kubernetes.io/managed-by": "k8s-wif-webhook",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			s.secretKey: credentialsData,
		},
	}

	// Try to get existing secret
	existingSecret := &corev1.Secret{}
	err := s.client.Get(ctx, client.ObjectKey{
		Name:      s.secretName,
		Namespace: namespace,
	}, existingSecret)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Create new secret
			secretSyncerLog.Info("Creating development credentials secret in namespace",
				"secret", s.secretName,
				"namespace", namespace)
			return s.client.Create(ctx, secret)
		}
		return fmt.Errorf("failed to get existing secret in namespace %s: %w", namespace, err)
	}

	// Update existing secret
	existingSecret.Data = secret.Data
	existingSecret.Labels = secret.Labels

	secretSyncerLog.Info("Updating development credentials secret in namespace",
		"secret", s.secretName,
		"namespace", namespace)
	return s.client.Update(ctx, existingSecret)
}
