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

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/groq/k8s-wif-webhook/internal/wif"
)

const (
	directIdentityConfigMapName = "wif-credentials-direct"
)

var (
	// directConfigMapOperations tracks direct identity ConfigMap operations during reconciliation
	// This provides finer granularity than the built-in controller_runtime_reconcile_total metric
	directConfigMapOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wif_direct_configmap_operations_total",
			Help: "Total number of direct identity ConfigMap operations",
		},
		[]string{"operation"}, // create, update, noop
	)
)

func init() {
	// Note: controller-runtime automatically provides:
	// - controller_runtime_reconcile_total{controller="Namespace",result="success|error|requeue"}
	// - controller_runtime_reconcile_errors_total{controller="Namespace"}
	// - controller_runtime_reconcile_time_seconds{controller="Namespace"}
	metrics.Registry.MustRegister(directConfigMapOperations)
}

// NamespaceReconciler reconciles Namespace objects and ensures direct identity ConfigMaps exist
type NamespaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// SetupWithManager sets up the controller with the Manager
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		// Note: Cannot use Owns() here - Namespaces cannot own resources within themselves
		// ConfigMaps will be cleaned up automatically when the namespace is deleted
		Complete(r)
}

// Reconcile ensures the direct identity ConfigMap exists in each namespace
func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Namespace
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			// Namespace deleted - ConfigMap will be cleaned up automatically
			log.V(1).Info("Namespace not found, likely deleted", "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Skip terminating namespaces
	if namespace.Status.Phase == corev1.NamespaceTerminating {
		log.V(1).Info("Skipping terminating namespace", "namespace", namespace.Name)
		return ctrl.Result{}, nil
	}

	// Skip namespaces that have explicitly opted out of WIF injection
	// This prevents credential access even via manual ConfigMap mounting (security requirement)
	if r.isWIFInjectionDisabled(namespace) {
		log.V(1).Info("Skipping namespace with WIF injection disabled", "namespace", namespace.Name)
		if r.Recorder != nil {
			r.Recorder.Event(namespace, corev1.EventTypeNormal, "ReconciliationSkipped",
				"WIF injection disabled - skipping direct identity ConfigMap reconciliation")
		}
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling direct identity ConfigMap for namespace", "namespace", namespace.Name)

	// Reconcile the direct identity ConfigMap
	if err := r.reconcileDirectConfigMap(ctx, namespace); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// isWIFInjectionDisabled checks if WIF injection is disabled at the namespace level
// This matches the webhook's opt-out behavior to prevent credential access
func (r *NamespaceReconciler) isWIFInjectionDisabled(namespace *corev1.Namespace) bool {
	if namespace.Labels == nil {
		return false
	}

	// Check namespace-level opt-out label (matches webhook behavior)
	if inject, exists := namespace.Labels["workload-identity.io/injection"]; exists && inject == "disabled" {
		return true
	}

	return false
}

// reconcileDirectConfigMap ensures the direct identity ConfigMap exists and is up-to-date
func (r *NamespaceReconciler) reconcileDirectConfigMap(ctx context.Context, namespace *corev1.Namespace) error {
	log := log.FromContext(ctx)

	configMapKey := types.NamespacedName{
		Name:      directIdentityConfigMapName,
		Namespace: namespace.Name,
	}

	// Check if ConfigMap exists
	existingCM := &corev1.ConfigMap{}
	err := r.Get(ctx, configMapKey, existingCM)

	// Generate expected ConfigMap
	desiredCM := r.buildDirectConfigMap(namespace)

	if apierrors.IsNotFound(err) {
		// ConfigMap doesn't exist - create it
		log.Info("Creating direct identity ConfigMap",
			"configMap", directIdentityConfigMapName,
			"namespace", namespace.Name)

		if err := r.Create(ctx, desiredCM); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Race condition - another reconciliation created it
				log.V(1).Info("ConfigMap already exists (race condition)", "configMap", directIdentityConfigMapName)
				directConfigMapOperations.WithLabelValues("noop").Inc()
				return nil
			}
			directConfigMapOperations.WithLabelValues("error").Inc()
			if r.Recorder != nil {
				r.Recorder.Event(namespace, corev1.EventTypeWarning, "ConfigMapCreateFailed",
					fmt.Sprintf("Failed to create direct identity ConfigMap: %v", err))
			}
			return fmt.Errorf("failed to create ConfigMap %s/%s: %w", namespace.Name, directIdentityConfigMapName, err)
		}

		directConfigMapOperations.WithLabelValues("create").Inc()
		if r.Recorder != nil {
			r.Recorder.Event(namespace, corev1.EventTypeNormal, "ConfigMapCreated",
				fmt.Sprintf("Created direct identity ConfigMap '%s'", directIdentityConfigMapName))
		}
		log.Info("Successfully created direct identity ConfigMap",
			"configMap", directIdentityConfigMapName,
			"namespace", namespace.Name)
		return nil
	}

	if err != nil {
		// Some other error occurred
		return fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace.Name, directIdentityConfigMapName, err)
	}

	// ConfigMap exists - check if it needs updating
	if r.configMapNeedsUpdate(existingCM, desiredCM) {
		log.Info("Updating direct identity ConfigMap",
			"configMap", directIdentityConfigMapName,
			"namespace", namespace.Name)

		// Update the ConfigMap
		existingCM.Data = desiredCM.Data
		existingCM.Labels = desiredCM.Labels

		if err := r.Update(ctx, existingCM); err != nil {
			directConfigMapOperations.WithLabelValues("error").Inc()
			if r.Recorder != nil {
				r.Recorder.Event(namespace, corev1.EventTypeWarning, "ConfigMapUpdateFailed",
					fmt.Sprintf("Failed to update direct identity ConfigMap: %v", err))
			}
			return fmt.Errorf("failed to update ConfigMap %s/%s: %w", namespace.Name, directIdentityConfigMapName, err)
		}

		directConfigMapOperations.WithLabelValues("update").Inc()
		if r.Recorder != nil {
			r.Recorder.Event(namespace, corev1.EventTypeNormal, "ConfigMapUpdated",
				fmt.Sprintf("Updated direct identity ConfigMap '%s' (self-healing)", directIdentityConfigMapName))
		}
		log.Info("Successfully updated direct identity ConfigMap",
			"configMap", directIdentityConfigMapName,
			"namespace", namespace.Name)
		return nil
	}

	// ConfigMap is up-to-date
	log.V(1).Info("ConfigMap is up-to-date, no changes needed",
		"configMap", directIdentityConfigMapName,
		"namespace", namespace.Name)
	directConfigMapOperations.WithLabelValues("noop").Inc()
	return nil
}

// buildDirectConfigMap constructs the desired direct identity ConfigMap for the namespace
func (r *NamespaceReconciler) buildDirectConfigMap(namespace *corev1.Namespace) *corev1.ConfigMap {
	// Direct identity configuration
	config := &wif.WIFConfig{
		UseDirectIdentity:    true,
		CredentialsConfigMap: directIdentityConfigMapName,
	}

	credentialsData := wif.GenerateCredentialsConfig(config)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      directIdentityConfigMapName,
			Namespace: namespace.Name,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
				"workload-identity.io/type":    "direct",
			},
		},
		Data: map[string]string{
			"credentials.json": credentialsData,
		},
	}

	// Note: No owner reference - ConfigMap is shared across all pods in namespace
	// It will be cleaned up when the namespace is deleted

	return cm
}

// configMapNeedsUpdate determines if the existing ConfigMap needs to be updated
func (r *NamespaceReconciler) configMapNeedsUpdate(existing, desired *corev1.ConfigMap) bool {
	// Check credentials data
	if existing.Data == nil || existing.Data["credentials.json"] != desired.Data["credentials.json"] {
		return true
	}

	// Check labels
	if existing.Labels == nil {
		return true
	}
	if existing.Labels["app.kubernetes.io/managed-by"] != desired.Labels["app.kubernetes.io/managed-by"] {
		return true
	}
	if existing.Labels["workload-identity.io/type"] != desired.Labels["workload-identity.io/type"] {
		return true
	}

	return false
}
