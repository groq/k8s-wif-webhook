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

// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/groq/k8s-wif-webhook/internal/wif"
)

var (
	// configMapReconcileOperations tracks ConfigMap operations during reconciliation
	// This provides finer granularity than the built-in controller_runtime_reconcile_total metric
	configMapReconcileOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wif_configmap_reconcile_operations_total",
			Help: "Total number of ConfigMap reconciliation operations",
		},
		[]string{"operation"}, // create, update, noop, delete
	)
)

func init() {
	// Note: controller-runtime automatically provides:
	// - controller_runtime_reconcile_total{controller="ServiceAccount",result="success|error|requeue"}
	// - controller_runtime_reconcile_errors_total{controller="ServiceAccount"}
	// - controller_runtime_reconcile_time_seconds{controller="ServiceAccount"}
	metrics.Registry.MustRegister(configMapReconcileOperations)
}

// ServiceAccountReconciler reconciles ServiceAccount objects with WIF annotations
type ServiceAccountReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// SetupWithManager sets up the controller with the Manager
func (r *ServiceAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

// Reconcile handles ServiceAccount changes and manages impersonation ConfigMaps
func (r *ServiceAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the ServiceAccount
	sa := &corev1.ServiceAccount{}
	if err := r.Get(ctx, req.NamespacedName, sa); err != nil {
		if apierrors.IsNotFound(err) {
			// ServiceAccount deleted - ConfigMap will be cleaned up via owner reference
			log.V(1).Info("ServiceAccount not found, likely deleted", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if namespace has opted out of WIF injection
	// This prevents credential access even via manual ConfigMap mounting (security requirement)
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: sa.Namespace}, namespace); err != nil {
		log.Error(err, "Failed to get namespace for opt-out check", "namespace", sa.Namespace)
		return ctrl.Result{}, err
	}

	if r.isWIFInjectionDisabled(namespace) {
		log.V(1).Info("Skipping ServiceAccount in namespace with WIF injection disabled",
			"serviceAccount", sa.Name, "namespace", sa.Namespace)
		if r.Recorder != nil {
			r.Recorder.Event(sa, corev1.EventTypeNormal, "ReconciliationSkipped",
				"WIF injection disabled in namespace - skipping impersonation ConfigMap reconciliation")
		}
		return ctrl.Result{}, nil
	}

	// Extract WIF configuration
	config := wif.ExtractWIFConfig(sa)

	// Only reconcile impersonation ConfigMaps (direct identity is handled by namespace controller)
	if config.UseDirectIdentity {
		log.V(1).Info("ServiceAccount uses direct identity, no ConfigMap to reconcile",
			"serviceAccount", sa.Name, "namespace", sa.Namespace)
		return ctrl.Result{}, nil
	}

	// Check if ServiceAccount has impersonation annotation
	gcpSA, hasAnnotation := sa.Annotations["iam.gke.io/gcp-service-account"]
	if !hasAnnotation {
		// This shouldn't happen if ExtractWIFConfig works correctly, but double-check
		log.V(1).Info("ServiceAccount missing impersonation annotation",
			"serviceAccount", sa.Name, "namespace", sa.Namespace)
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling impersonation ConfigMap for ServiceAccount",
		"serviceAccount", sa.Name,
		"namespace", sa.Namespace,
		"gcpServiceAccount", gcpSA,
		"configMap", config.CredentialsConfigMap)

	// Reconcile the ConfigMap
	if err := r.reconcileConfigMap(ctx, sa, config); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// isWIFInjectionDisabled checks if WIF injection is disabled at the namespace level
// This matches the webhook's opt-out behavior to prevent credential access
func (r *ServiceAccountReconciler) isWIFInjectionDisabled(namespace *corev1.Namespace) bool {
	if namespace.Labels == nil {
		return false
	}

	// Check namespace-level opt-out label (matches webhook behavior)
	if inject, exists := namespace.Labels["workload-identity.io/injection"]; exists && inject == "disabled" {
		return true
	}

	return false
}

// reconcileConfigMap ensures the impersonation ConfigMap exists and is up-to-date
func (r *ServiceAccountReconciler) reconcileConfigMap(ctx context.Context, sa *corev1.ServiceAccount, config *wif.WIFConfig) error {
	log := log.FromContext(ctx)

	configMapKey := types.NamespacedName{
		Name:      config.CredentialsConfigMap,
		Namespace: sa.Namespace,
	}

	// Check if ConfigMap exists
	existingCM := &corev1.ConfigMap{}
	err := r.Get(ctx, configMapKey, existingCM)

	// Generate expected ConfigMap
	desiredCM := r.buildConfigMap(sa, config)

	if apierrors.IsNotFound(err) {
		// ConfigMap doesn't exist - create it
		log.Info("Creating impersonation ConfigMap",
			"configMap", config.CredentialsConfigMap,
			"namespace", sa.Namespace,
			"gcpServiceAccount", config.GoogleServiceAccount)

		if err := r.Create(ctx, desiredCM); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Race condition - another reconciliation created it
				log.V(1).Info("ConfigMap already exists (race condition)", "configMap", config.CredentialsConfigMap)
				configMapReconcileOperations.WithLabelValues("noop").Inc()
				return nil
			}
			configMapReconcileOperations.WithLabelValues("error").Inc()
			if r.Recorder != nil {
				r.Recorder.Event(sa, corev1.EventTypeWarning, "ConfigMapCreateFailed",
					fmt.Sprintf("Failed to create impersonation ConfigMap '%s': %v", config.CredentialsConfigMap, err))
			}
			return fmt.Errorf("failed to create ConfigMap %s/%s: %w", sa.Namespace, config.CredentialsConfigMap, err)
		}

		configMapReconcileOperations.WithLabelValues("create").Inc()
		if r.Recorder != nil {
			r.Recorder.Event(sa, corev1.EventTypeNormal, "ConfigMapCreated",
				fmt.Sprintf("Created impersonation ConfigMap '%s' for GCP service account '%s'",
					config.CredentialsConfigMap, config.GoogleServiceAccount))
		}
		log.Info("Successfully created impersonation ConfigMap",
			"configMap", config.CredentialsConfigMap,
			"namespace", sa.Namespace)
		return nil
	}

	if err != nil {
		// Some other error occurred
		return fmt.Errorf("failed to get ConfigMap %s/%s: %w", sa.Namespace, config.CredentialsConfigMap, err)
	}

	// ConfigMap exists - check if it needs updating
	if r.configMapNeedsUpdate(existingCM, desiredCM) {
		log.Info("Updating impersonation ConfigMap",
			"configMap", config.CredentialsConfigMap,
			"namespace", sa.Namespace,
			"gcpServiceAccount", config.GoogleServiceAccount)

		// Update the ConfigMap
		existingCM.Data = desiredCM.Data
		existingCM.Labels = desiredCM.Labels
		existingCM.OwnerReferences = desiredCM.OwnerReferences

		if err := r.Update(ctx, existingCM); err != nil {
			configMapReconcileOperations.WithLabelValues("error").Inc()
			if r.Recorder != nil {
				r.Recorder.Event(sa, corev1.EventTypeWarning, "ConfigMapUpdateFailed",
					fmt.Sprintf("Failed to update impersonation ConfigMap '%s': %v", config.CredentialsConfigMap, err))
			}
			return fmt.Errorf("failed to update ConfigMap %s/%s: %w", sa.Namespace, config.CredentialsConfigMap, err)
		}

		configMapReconcileOperations.WithLabelValues("update").Inc()
		if r.Recorder != nil {
			r.Recorder.Event(sa, corev1.EventTypeNormal, "ConfigMapUpdated",
				fmt.Sprintf("Updated impersonation ConfigMap '%s' for GCP service account '%s' (annotation change or self-healing)",
					config.CredentialsConfigMap, config.GoogleServiceAccount))
		}
		log.Info("Successfully updated impersonation ConfigMap",
			"configMap", config.CredentialsConfigMap,
			"namespace", sa.Namespace)
		return nil
	}

	// ConfigMap is up-to-date
	log.V(1).Info("ConfigMap is up-to-date, no changes needed",
		"configMap", config.CredentialsConfigMap,
		"namespace", sa.Namespace)
	configMapReconcileOperations.WithLabelValues("noop").Inc()
	return nil
}

// buildConfigMap constructs the desired ConfigMap for the given ServiceAccount and WIF config
func (r *ServiceAccountReconciler) buildConfigMap(sa *corev1.ServiceAccount, config *wif.WIFConfig) *corev1.ConfigMap {
	credentialsData := wif.GenerateCredentialsConfig(config)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.CredentialsConfigMap,
			Namespace: sa.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "k8s-native-wif-webhook",
				"workload-identity.io/type":    wif.GetConfigMapType(config),
			},
		},
		Data: map[string]string{
			"credentials.json": credentialsData,
		},
	}

	// Set ServiceAccount as owner for automatic cleanup
	controller := true
	cm.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
			Name:       sa.Name,
			UID:        sa.UID,
			Controller: &controller,
		},
	}

	return cm
}

// configMapNeedsUpdate determines if the existing ConfigMap needs to be updated
func (r *ServiceAccountReconciler) configMapNeedsUpdate(existing, desired *corev1.ConfigMap) bool {
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

	// Check owner references
	if !controllerutil.HasControllerReference(existing) {
		return true
	}

	// Compare owner reference (ServiceAccount UID might have changed)
	if len(existing.OwnerReferences) != len(desired.OwnerReferences) {
		return true
	}

	for i := range existing.OwnerReferences {
		existingRef := existing.OwnerReferences[i]
		desiredRef := desired.OwnerReferences[i]

		if existingRef.UID != desiredRef.UID ||
			existingRef.Name != desiredRef.Name ||
			existingRef.Kind != desiredRef.Kind {
			return true
		}
	}

	return false
}
