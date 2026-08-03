/*
Copyright 2026.

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
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	qiujian16githubcomv1 "github.com/qiujian16/namespace-class/api/v1"
)

// relatedResource represents a single resource that was applied to a namespace.
type relatedResource struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	Status     metav1.Condition `json:"status,omitzero"`
}

const (
	// Apply status condition type and reasons.
	applyConditionType             = "Applied"
	applyReasonResourceApplied     = "ResourceApplied"
	applyReasonResourceApplyFailed = "ResourceApplyFailed"

	// NamespaceFinalizer is the finalizer added to namespaces managed by this controller.
	// It blocks namespace deletion until the applied resources have been cleaned up.
	NamespaceFinalizer = "qiujian16.github.com/namespace-finalizer"
)

// ResourceApplyStrategy defines how resources are applied to the cluster.
// Implementations may use full updates, server-side apply, or other strategies.
type ResourceApplyStrategy interface {
	Apply(ctx context.Context, dynamicClient dynamic.Interface, restMapper meta.RESTMapper, obj *unstructured.Unstructured) error
}

// UpdateApplyStrategy implements ResourceApplyStrategy by first getting the
// existing resource and then creating if absent or fully replacing if present.
type UpdateApplyStrategy struct{}

// Apply implements ResourceApplyStrategy. It first attempts to Get the resource;
// if not found it Creates it, if found and equal it skips, otherwise it Updates.
func (s *UpdateApplyStrategy) Apply(ctx context.Context, dynamicClient dynamic.Interface, restMapper meta.RESTMapper, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()

	mapping, err := restMapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to find REST mapping for %s: %w", gvk, err)
	}

	resource := dynamicClient.Resource(mapping.Resource).Namespace(obj.GetNamespace())

	// Get first
	existing, err := resource.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
		}
		// Not found — create
		if _, err := resource.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
		}
		logf.FromContext(ctx).Info("created resource", "kind", gvk.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName())
		return nil
	}

	// Compare existing and desired (excluding status) — skip update if equal
	if equalWithoutStatus(existing, obj) {
		logf.FromContext(ctx).V(1).Info("resource unchanged, skipping update",
			"kind", gvk.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName())
		return nil
	}

	// Exists and differs — update
	obj.SetResourceVersion(existing.GetResourceVersion())
	if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
	}
	logf.FromContext(ctx).Info("updated resource", "kind", gvk.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName())
	return nil
}

// equalWithoutStatus compares two unstructured objects for equality.
// Status is stripped, and within metadata only labels and annotations are
// compared — fields like resourceVersion, uid, and managedFields are ignored
// so they don't trigger unnecessary updates.
func equalWithoutStatus(a, b *unstructured.Unstructured) bool {
	aCopy := a.DeepCopy()
	bCopy := b.DeepCopy()

	// Strip status
	unstructured.RemoveNestedField(aCopy.Object, "status")
	unstructured.RemoveNestedField(bCopy.Object, "status")

	// Reduce metadata to only labels and annotations
	aCopy.Object["metadata"] = filterMetadata(aCopy.Object["metadata"])
	bCopy.Object["metadata"] = filterMetadata(bCopy.Object["metadata"])

	return apiequality.Semantic.DeepEqual(aCopy.Object, bCopy.Object)
}

// filterMetadata extracts only labels and annotations from a metadata map.
// Returns nil if both are empty.
func filterMetadata(raw any) any {
	m, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	filtered := make(map[string]any)
	if labels, exists := m["labels"]; exists {
		filtered["labels"] = labels
	}
	if annotations, exists := m["annotations"]; exists {
		filtered["annotations"] = annotations
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// NamespaceReconciler reconciles Namespace objects that reference a NamespaceClass.
type NamespaceReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	DynamicClient dynamic.Interface
	RESTMapper    meta.RESTMapper
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=qiujian16.github.com.qiujian16.github.com,resources=namespaceclasses,verbs=get;list;watch

// Reconcile reconciles a Namespace. It finds the related NamespaceClass,
// applies its manifests to the namespace, diffs against the annotation to
// remove stale resources, and updates the annotation with the current list.
func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Get the namespace
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion — clean up applied resources and remove finalizer
	if !ns.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, ns)
	}

	// Check if this namespace references a NamespaceClass
	namespaceClassName, hasLabel := ns.Labels[NamespaceClassLabelKey]
	if !hasLabel || namespaceClassName == "" {
		// No reference — clean up applied resources and remove finalizer if present
		latest, err := r.cleanupAllResourcesForNamespace(ctx, ns.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to cleanup resources for namespace %s: %w", ns.Name, err)
		}
		if latest != nil && controllerutil.ContainsFinalizer(latest, NamespaceFinalizer) {
			controllerutil.RemoveFinalizer(latest, NamespaceFinalizer)
			if err := r.Update(ctx, latest); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from namespace %s: %w", ns.Name, err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present so we can clean up on deletion
	if !controllerutil.ContainsFinalizer(ns, NamespaceFinalizer) {
		controllerutil.AddFinalizer(ns, NamespaceFinalizer)
		if err := r.Update(ctx, ns); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer to namespace %s: %w", ns.Name, err)
		}
	}

	// Get the referenced NamespaceClass
	namespaceClass := &qiujian16githubcomv1.NamespaceClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespaceClassName}, namespaceClass); err != nil {
		if apierrors.IsNotFound(err) {
			// NamespaceClass is gone — delete all applied resources before removing the annotation
			logger.Info("NamespaceClass not found, cleaning up resources and annotation",
				"namespaceclass", namespaceClassName, "namespace", ns.Name)
			if _, err := r.cleanupAllResourcesForNamespace(ctx, ns.Name); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to cleanup resources for namespace %s: %w", ns.Name, err)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get namespaceclass %s: %w", namespaceClassName, err)
	}

	// Apply manifests and build the desired resource list with apply status.
	var desiredResources []relatedResource
	var errs []error
	for _, manifest := range namespaceClass.Spec.Policies.Manifests {
		res, err := r.applyManifestToNamespace(ctx, manifest, ns.Name)
		if err != nil {
			logger.Error(err, "failed to apply manifest to namespace", "namespace", ns.Name)
			errs = append(errs, err)
			res = resourceFromManifest(manifest)
			res.Status = metav1.Condition{
				Type:    applyConditionType,
				Status:  metav1.ConditionFalse,
				Reason:  applyReasonResourceApplyFailed,
				Message: err.Error(),
			}
		} else {
			res.Status = metav1.Condition{
				Type:   applyConditionType,
				Status: metav1.ConditionTrue,
				Reason: applyReasonResourceApplied,
			}
		}
		desiredResources = append(desiredResources, res)
	}

	// Sort for stable comparison and annotation.
	slices.SortFunc(desiredResources, func(a, b relatedResource) int {
		if resourceKey(a) < resourceKey(b) {
			return -1
		}
		if resourceKey(a) > resourceKey(b) {
			return 1
		}
		return 0
	})

	// Parse the existing annotation
	existingResources := parseRelatedResourcesAnnotation(ns)

	// Find resources to remove (in existing but not in desired)
	toRemove := diffResources(existingResources, desiredResources)

	// Delete resources that are no longer in the NamespaceClass spec.
	// Track failures so they remain in the annotation and are retried next reconcile.
	var failedDeletes []relatedResource
	for _, res := range toRemove {
		logger.Info("removing stale resource from namespace",
			"kind", res.Kind, "name", res.Name, "namespace", ns.Name)
		if err := r.deleteResourceFromNamespace(ctx, res, ns.Name); err != nil {
			logger.Error(err, "failed to delete stale resource", "namespace", ns.Name, "kind", res.Kind, "name", res.Name)
			errs = append(errs, err)
			failedDeletes = append(failedDeletes, res)
		}
	}

	// Annotation records desiredResources (all resources we want to exist)
	// plus failedDeletes (resources we failed to delete, which still exist
	// and need cleanup). Together they reflect the actual cluster state.
	annotationResources := append(desiredResources, failedDeletes...)
	if err := r.updateRelatedResourcesAnnotation(ctx, ns, annotationResources); err != nil {
		logger.Error(err, "failed to update related resources annotation", "namespace", ns.Name)
		errs = append(errs, err)
	}

	result := ctrl.Result{}
	if len(errs) == 0 {
		// Requeue with jitter so we re-check in case another actor
		// changed the resources back.
		jitter := time.Duration(rand.Intn(2000)) * time.Millisecond
		result.RequeueAfter = 5*time.Second + jitter
	}

	return result, errors.Join(errs...)
}

// reconcileDelete handles namespace deletion. It cleans up all applied resources
// and removes the finalizer so the namespace can be fully deleted.
func (r *NamespaceReconciler) reconcileDelete(ctx context.Context, ns *corev1.Namespace) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(ns, NamespaceFinalizer) {
		return ctrl.Result{}, nil
	}

	logger.Info("namespace is being deleted, cleaning up applied resources", "namespace", ns.Name)

	latest, err := r.cleanupAllResourcesForNamespace(ctx, ns.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to cleanup resources for deleting namespace %s: %w", ns.Name, err)
	}
	if latest == nil {
		// Namespace is already gone.
		return ctrl.Result{}, nil
	}

	controllerutil.RemoveFinalizer(latest, NamespaceFinalizer)
	if err := r.Update(ctx, latest); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from namespace %s: %w", ns.Name, err)
	}

	logger.Info("removed finalizer from namespace, deletion can proceed", "namespace", ns.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// It watches Namespaces with the NamespaceClass label, and also watches
// NamespaceClass changes to enqueue related namespaces.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				_, ok := e.Object.GetLabels()[NamespaceClassLabelKey]
				return ok
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				_, oldOk := e.ObjectOld.GetLabels()[NamespaceClassLabelKey]
				_, newOk := e.ObjectNew.GetLabels()[NamespaceClassLabelKey]
				return oldOk || newOk
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				_, ok := e.Object.GetLabels()[NamespaceClassLabelKey]
				return ok
			},
			GenericFunc: func(e event.GenericEvent) bool {
				_, ok := e.Object.GetLabels()[NamespaceClassLabelKey]
				return ok
			},
		})).
		Watches(
			&qiujian16githubcomv1.NamespaceClass{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueNamespacesForNamespaceClass),
		).
		Named("namespace").
		Complete(r)
}

// enqueueNamespacesForNamespaceClass lists all namespaces that reference the
// given NamespaceClass and enqueues them for reconciliation.
func (r *NamespaceReconciler) enqueueNamespacesForNamespaceClass(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := logf.FromContext(ctx)

	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, client.MatchingLabels{
		NamespaceClassLabelKey: obj.GetName(),
	}); err != nil {
		logger.Error(err, "failed to list namespaces for namespaceclass",
			"namespaceclass", obj.GetName())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: ns.Name},
		})
	}
	return requests
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// resourceFromManifest extracts apiVersion, kind, and name from a raw manifest.
// The returned resource has no status set.
func resourceFromManifest(manifest qiujian16githubcomv1.Manifest) relatedResource {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(manifest.Raw); err != nil {
		return relatedResource{}
	}
	gvk := obj.GroupVersionKind()
	return relatedResource{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Name:       obj.GetName(),
	}
}

// parseRelatedResourcesAnnotation parses the NamespaceClassRelatedResourcesAnnotationKey
// annotation from a namespace. Returns an empty slice if the annotation is
// missing or unparseable.
func parseRelatedResourcesAnnotation(ns *corev1.Namespace) []relatedResource {
	raw, ok := ns.Annotations[NamespaceClassRelatedResourcesAnnotationKey]
	if !ok || raw == "" {
		return nil
	}

	var resources []relatedResource
	if err := json.Unmarshal([]byte(raw), &resources); err != nil {
		return nil
	}
	return resources
}

// diffResources returns resources that are in existing but not in desired.
func diffResources(existing, desired []relatedResource) []relatedResource {
	desiredSet := make(map[string]bool, len(desired))
	for _, r := range desired {
		desiredSet[resourceKey(r)] = true
	}

	var toRemove []relatedResource
	for _, r := range existing {
		if !desiredSet[resourceKey(r)] {
			toRemove = append(toRemove, r)
		}
	}
	return toRemove
}

// resourceKey returns a unique key for a relatedResource.
func resourceKey(r relatedResource) string {
	return r.APIVersion + "/" + r.Kind + "/" + r.Name
}

// applyManifestToNamespace unmarshals a raw manifest, sets the namespace,
// and applies it using the update strategy (get-first, then create or update).
// TODO: strategy selection should come from the NamespaceClass API.
func (r *NamespaceReconciler) applyManifestToNamespace(ctx context.Context, manifest qiujian16githubcomv1.Manifest, namespace string) (relatedResource, error) {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(manifest.Raw); err != nil {
		return relatedResource{}, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	obj.SetNamespace(namespace)

	gvk := obj.GroupVersionKind()

	var strategy ResourceApplyStrategy = &UpdateApplyStrategy{}
	if err := strategy.Apply(ctx, r.DynamicClient, r.RESTMapper, obj); err != nil {
		return relatedResource{}, err
	}

	return relatedResource{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Name:       obj.GetName(),
	}, nil
}

// deleteResourceFromNamespace deletes a single resource identified by the
// relatedResource reference from the given namespace.
func (r *NamespaceReconciler) deleteResourceFromNamespace(ctx context.Context, res relatedResource, namespace string) error {
	gv, err := schema.ParseGroupVersion(res.APIVersion)
	if err != nil {
		return fmt.Errorf("failed to parse apiVersion %q: %w", res.APIVersion, err)
	}

	return dynamicDelete(ctx, r.DynamicClient, r.RESTMapper, gv.WithKind(res.Kind), namespace, res.Name)
}

// cleanupAllResourcesForNamespace deletes all resources recorded in the
// NamespaceClassRelatedResourcesAnnotationKey annotation from the given
// namespace, then removes the annotation. It continues on individual delete
// errors and returns an aggregated error.
// On success it returns the latest version of the namespace (after annotation removal).
func (r *NamespaceReconciler) cleanupAllResourcesForNamespace(ctx context.Context, namespace string) (*corev1.Namespace, error) {
	logger := logf.FromContext(ctx)

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get namespace %s: %w", namespace, err)
	}

	existingResources := parseRelatedResourcesAnnotation(ns)
	if len(existingResources) == 0 {
		return ns, nil
	}

	logger.Info("cleaning up all applied resources from namespace", "namespace", namespace)

	var errs []error
	for _, res := range existingResources {
		logger.Info("deleting resource from namespace",
			"kind", res.Kind, "name", res.Name, "namespace", namespace)
		if err := r.deleteResourceFromNamespace(ctx, res, namespace); err != nil {
			logger.Error(err, "failed to delete resource",
				"kind", res.Kind, "name", res.Name, "namespace", namespace)
			errs = append(errs, err)
		}
	}

	// Re-fetch and remove the annotation
	latest := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, latest); err != nil {
		errs = append(errs, fmt.Errorf("failed to re-fetch namespace %s to remove annotation: %w", namespace, err))
		return nil, errors.Join(errs...)
	}

	annotations := latest.GetAnnotations()
	if annotations != nil {
		delete(annotations, NamespaceClassRelatedResourcesAnnotationKey)
		latest.SetAnnotations(annotations)
		if err := r.Update(ctx, latest); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove annotation from namespace %s: %w", namespace, err))
			return nil, errors.Join(errs...)
		}
	}

	return latest, nil
}

// updateRelatedResourcesAnnotation updates the NamespaceClassRelatedResourcesAnnotationKey
// annotation on the namespace with the current list of applied resources.
func (r *NamespaceReconciler) updateRelatedResourcesAnnotation(ctx context.Context, ns *corev1.Namespace, resources []relatedResource) error {
	raw, err := json.Marshal(resources)
	if err != nil {
		return fmt.Errorf("failed to marshal related resources: %w", err)
	}

	annotations := ns.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if string(raw) == "null" || string(raw) == "[]" {
		delete(annotations, NamespaceClassRelatedResourcesAnnotationKey)
	} else {
		annotations[NamespaceClassRelatedResourcesAnnotationKey] = string(raw)
	}
	ns.SetAnnotations(annotations)

	if err := r.Update(ctx, ns); err != nil {
		return fmt.Errorf("failed to update namespace %s annotation: %w", ns.Name, err)
	}

	return nil
}
