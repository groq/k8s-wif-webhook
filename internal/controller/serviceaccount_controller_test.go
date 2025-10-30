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

func TestServiceAccountReconciler_Reconcile(t *testing.T) {
	require.NoError(t, os.Setenv("WORKLOAD_IDENTITY_PROVIDER", "projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider"))
	defer os.Unsetenv("WORKLOAD_IDENTITY_PROVIDER")

	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))

	t.Run("creates ConfigMap for ServiceAccount with impersonation annotation", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
		}

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
				UID:       types.UID("test-uid-123"),
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "test@project.iam.gserviceaccount.com",
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, sa).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was created
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-impersonation-test-sa",
			Namespace: "default",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		// Verify ConfigMap contents
		assert.Equal(t, "k8s-native-wif-webhook", cm.Labels["app.kubernetes.io/managed-by"])
		assert.Equal(t, "impersonation", cm.Labels["workload-identity.io/type"])
		assert.Contains(t, cm.Data["credentials.json"], "test@project.iam.gserviceaccount.com")

		// Verify owner reference
		require.Len(t, cm.OwnerReferences, 1)
		assert.Equal(t, "ServiceAccount", cm.OwnerReferences[0].Kind)
		assert.Equal(t, sa.Name, cm.OwnerReferences[0].Name)
		assert.Equal(t, sa.UID, cm.OwnerReferences[0].UID)
		assert.True(t, *cm.OwnerReferences[0].Controller)
	})

	t.Run("updates ConfigMap when ServiceAccount annotation changes", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
		}

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
				UID:       types.UID("test-uid-456"),
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "test@project.iam.gserviceaccount.com",
				},
			},
		}

		// Create existing ConfigMap with old GCP SA
		oldConfig := &wif.WIFConfig{
			UseDirectIdentity:    false,
			GoogleServiceAccount: "old@project.iam.gserviceaccount.com",
			CredentialsConfigMap: "wif-credentials-impersonation-test-sa",
		}

		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wif-credentials-impersonation-test-sa",
				Namespace: "default",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       sa.Name,
						UID:        sa.UID,
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": wif.GenerateCredentialsConfig(oldConfig),
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, sa, existingCM).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was updated
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-impersonation-test-sa",
			Namespace: "default",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		// Verify ConfigMap contains new GCP SA
		assert.Contains(t, cm.Data["credentials.json"], "test@project.iam.gserviceaccount.com")
		assert.NotContains(t, cm.Data["credentials.json"], "old@project.iam.gserviceaccount.com")
	})

	t.Run("updates ConfigMap when ServiceAccount UID changes (recreated)", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
		}

		newUID := types.UID("new-uid-789")
		oldUID := types.UID("old-uid-123")

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
				UID:       newUID,
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "test@project.iam.gserviceaccount.com",
				},
			},
		}

		// ConfigMap with old UID in owner reference
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wif-credentials-impersonation-test-sa",
				Namespace: "default",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       sa.Name,
						UID:        oldUID, // Old UID
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": wif.GenerateCredentialsConfig(&wif.WIFConfig{
					UseDirectIdentity:    false,
					GoogleServiceAccount: "test@project.iam.gserviceaccount.com",
					CredentialsConfigMap: "wif-credentials-impersonation-test-sa",
				}),
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, sa, existingCM).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap owner reference was updated to new UID
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-impersonation-test-sa",
			Namespace: "default",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))

		require.Len(t, cm.OwnerReferences, 1)
		assert.Equal(t, newUID, cm.OwnerReferences[0].UID)
	})

	t.Run("skips ServiceAccount without impersonation annotation", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
		}

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
				UID:       types.UID("test-uid-999"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, sa).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify no ConfigMap was created
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-impersonation-test-sa",
			Namespace: "default",
		}
		err = client.Get(ctx, cmKey, cm)
		assert.Error(t, err, "ConfigMap should not exist")
	})

	t.Run("handles ServiceAccount deletion gracefully", func(t *testing.T) {
		ctx := context.Background()

		client := fake.NewClientBuilder().WithScheme(s).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      "deleted-sa",
				Namespace: "default",
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("skips ServiceAccount in opted-out namespace", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "opted-out-namespace",
				Labels: map[string]string{
					"workload-identity.io/injection": "disabled",
				},
			},
		}

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "opted-out-namespace",
				UID:       types.UID("test-uid-opted-out"),
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "test@project.iam.gserviceaccount.com",
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, sa).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		}

		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify ConfigMap was NOT created (security requirement)
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{
			Name:      "wif-credentials-impersonation-test-sa",
			Namespace: "opted-out-namespace",
		}
		err = client.Get(ctx, cmKey, cm)
		assert.Error(t, err, "ConfigMap should not exist in opted-out namespace")
	})

	t.Run("is idempotent when ConfigMap is up-to-date", func(t *testing.T) {
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
		}

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
				UID:       types.UID("test-uid-idempotent"),
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "test@project.iam.gserviceaccount.com",
				},
			},
		}

		config := wif.ExtractWIFConfig(sa)

		// ConfigMap already up-to-date
		upToDateCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      config.CredentialsConfigMap,
				Namespace: "default",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       sa.Name,
						UID:        sa.UID,
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": wif.GenerateCredentialsConfig(config),
			},
		}

		client := fake.NewClientBuilder().WithScheme(s).WithObjects(ns, sa, upToDateCM).Build()

		reconciler := &ServiceAccountReconciler{
			Client: client,
			Scheme: s,
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      sa.Name,
				Namespace: sa.Namespace,
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
			Name:      config.CredentialsConfigMap,
			Namespace: "default",
		}
		require.NoError(t, client.Get(ctx, cmKey, cm))
		assert.Equal(t, wif.GenerateCredentialsConfig(config), cm.Data["credentials.json"])
	})
}

func TestServiceAccountReconciler_configMapNeedsUpdate(t *testing.T) {
	reconciler := &ServiceAccountReconciler{}

	t.Run("returns false when ConfigMaps are identical", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       "test-sa",
						UID:        types.UID("test-uid"),
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": "test-credentials",
			},
		}

		desired := existing.DeepCopy()

		assert.False(t, reconciler.configMapNeedsUpdate(existing, desired))
	})

	t.Run("returns true when credentials data differs", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       "test-sa",
						UID:        types.UID("test-uid"),
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": "old-credentials",
			},
		}

		desired := existing.DeepCopy()
		desired.Data["credentials.json"] = "new-credentials"

		assert.True(t, reconciler.configMapNeedsUpdate(existing, desired))
	})

	t.Run("returns true when owner reference UID differs", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       "test-sa",
						UID:        types.UID("old-uid"),
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": "test-credentials",
			},
		}

		desired := existing.DeepCopy()
		desired.OwnerReferences[0].UID = types.UID("new-uid")

		assert.True(t, reconciler.configMapNeedsUpdate(existing, desired))
	})

	t.Run("returns true when labels are missing", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Labels: nil,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       "test-sa",
						UID:        types.UID("test-uid"),
						Controller: &[]bool{true}[0],
					},
				},
			},
			Data: map[string]string{
				"credentials.json": "test-credentials",
			},
		}

		desired := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
					"workload-identity.io/type":    "impersonation",
				},
				OwnerReferences: existing.OwnerReferences,
			},
			Data: existing.Data,
		}

		assert.True(t, reconciler.configMapNeedsUpdate(existing, desired))
	})
}
