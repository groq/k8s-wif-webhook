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

package controller

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/groq/k8s-wif-webhook/internal/wif"
)

func TestNamespaceReconciler_Reconcile(t *testing.T) {
	require.NoError(t, os.Setenv("WORKLOAD_IDENTITY_PROVIDER", "projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider"))
	defer os.Unsetenv("WORKLOAD_IDENTITY_PROVIDER")

	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))

	t.Run("creates direct identity ConfigMap for new namespace", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-namespace",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was created
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "test-namespace",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		// Verify ConfigMap contents
		assert.Equal(t, "k8s-native-wif-webhook", cm.Labels["app.kubernetes.io/managed-by"])
		assert.Equal(t, "direct", cm.Labels["workload-identity.io/type"])
		assert.Contains(t, cm.Data["credentials.json"], "external_account")
		assert.Contains(t, cm.Data["credentials.json"], "projects/123456789")

		// Verify no owner reference (shared resource)
		assert.Len(t, cm.OwnerReferences, 0)
	})

	t.Run("updates ConfigMap when WORKLOAD_IDENTITY_PROVIDER changes", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-namespace",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		}

		// Create existing ConfigMap with old provider
		oldProvider := "projects/old/locations/global/workloadIdentityPools/old-pool/providers/old-provider"
		os.Setenv("WORKLOAD_IDENTITY_PROVIDER", oldProvider)
		oldConfig := &wif.WIFConfig{
			UseDirectIdentity:    true,
			CredentialsConfigMap: "wif-credentials-direct",
		}
		oldCredentials := wif.GenerateCredentialsConfig(oldConfig)

		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wif-credentials-direct",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "direct",
				},
			},
			Data: map[string]string{
				"credentials.json": oldCredentials,
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, existingCM).Build()

		// Change provider (simulates webhook pod restart with new env var)
		newProvider := "projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider"
		os.Setenv("WORKLOAD_IDENTITY_PROVIDER", newProvider)

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was updated
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "test-namespace",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		// Verify ConfigMap contains new provider
		assert.Contains(t, cm.Data["credentials.json"], newProvider)
		assert.NotContains(t, cm.Data["credentials.json"], oldProvider)
	})

	t.Run("self-heals ConfigMap when data is corrupted", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-namespace",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		}

		// ConfigMap with corrupted data
		corruptedCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wif-credentials-direct",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "direct",
				},
			},
			Data: map[string]string{
				"credentials.json": `{"corrupted": "data"}`,
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, corruptedCM).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was healed
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "test-namespace",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		// Verify ConfigMap has correct structure
		assert.Contains(t, cm.Data["credentials.json"], "external_account")
		assert.Contains(t, cm.Data["credentials.json"], "audience")
		assert.NotContains(t, cm.Data["credentials.json"], `"corrupted"`)
	})

	t.Run("self-heals ConfigMap when labels are missing", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-namespace",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		}

		config := &wif.WIFConfig{
			UseDirectIdentity:    true,
			CredentialsConfigMap: "wif-credentials-direct",
		}

		// ConfigMap with missing labels
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wif-credentials-direct",
				Namespace: "test-namespace",
				Labels:    nil, // Missing labels
			},
			Data: map[string]string{
				"credentials.json": wif.GenerateCredentialsConfig(config),
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, existingCM).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify labels were restored
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "test-namespace",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		assert.Equal(t, "k8s-native-wif-webhook", cm.Labels["app.kubernetes.io/managed-by"])
		assert.Equal(t, "direct", cm.Labels["workload-identity.io/type"])
	})

	t.Run("skips terminating namespace", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "terminating-namespace",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceTerminating,
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify no ConfigMap was created
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "terminating-namespace",
		}
		err = client.Get(ctx, cmKey, cm)
		assert.Error(t, err, "ConfigMap should not exist")
	})

	t.Run("handles namespace deletion gracefully", func(t *testing.T) {
		ctx := context.Background()

		client := fake.NewClientBuilder().WithScheme(s).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: "deleted-namespace",
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("skips namespace with opt-out label", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "opted-out-namespace",
				Labels: map[string]string{
					"workload-identity.io/injection": "disabled",
				},
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was NOT created (security requirement)
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "opted-out-namespace",
		}
		err = client.Get(ctx, cmKey, cm)
		assert.Error(t, err, "ConfigMap should not exist in opted-out namespace")
	})

	t.Run("is idempotent when ConfigMap is up-to-date", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-namespace",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		}

		config := &wif.WIFConfig{
			UseDirectIdentity:    true,
			CredentialsConfigMap: "wif-credentials-direct",
		}

		// ConfigMap already up-to-date
		upToDateCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wif-credentials-direct",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "direct",
				},
			},
			Data: map[string]string{
				"credentials.json": wif.GenerateCredentialsConfig(config),
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, upToDateCM).Build()

		reconciler := &NamespaceReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name: ns.Name,
			},
		}

		// First reconciliation
		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Second reconciliation should be no-op
		result, err = reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap unchanged
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-direct",
			Namespace: "test-namespace",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))
		assert.Equal(t, wif.GenerateCredentialsConfig(config), cm.Data["credentials.json"])
	})
}
