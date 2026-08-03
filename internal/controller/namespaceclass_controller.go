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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	qiujian16githubcomv1 "github.com/qiujian16/namespace-class/api/v1"
)

const (
	NamespaceClassFinalizer = "qiujian16.github.com/namespace-class-finalizer"

	// NamespaceClassLabelKey is the label key on a namespace to specify the namespaceclass the namespace refers
	NamespaceClassLabelKey = "namespaceclass.akuity.io/name"

	// NamespaceClassRelatedResourcesAnnotationKey is the annotation key on a namespace representing the resource currently
	// applied together with this namespace relating to the specified namespaceclass on the label.
	NamespaceClassRelatedResourcesAnnotationKey = "namespaceclass.akuity.io/relatedresources"
)

// NamespaceClassReconciler reconciles a NamespaceClass object
type NamespaceClassReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	KubeClient    kubernetes.Interface
	DynamicClient dynamic.Interface
	RESTMapper    meta.RESTMapper
}

// +kubebuilder:rbac:groups=qiujian16.github.com.qiujian16.github.com,resources=namespaceclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qiujian16.github.com.qiujian16.github.com,resources=namespaceclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=qiujian16.github.com.qiujian16.github.com,resources=namespaceclasses/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NamespaceClass object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	namespaceClass := &qiujian16githubcomv1.NamespaceClass{}
	err := r.Get(ctx, req.NamespacedName, namespaceClass)
	if err != nil {
		logger.Error(err, "failed to get namespaceclass")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// examine DeletionTimestamp to determine if object is under deletion
	if namespaceClass.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then let's add the finalizer and update the object. This is equivalent
		// to registering our finalizer.
		if !controllerutil.ContainsFinalizer(namespaceClass, NamespaceClassFinalizer) {
			controllerutil.AddFinalizer(namespaceClass, NamespaceClassFinalizer)
			if err := r.Update(ctx, namespaceClass); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(namespaceClass, NamespaceClassFinalizer) {
			// our finalizer is present, so let's handle any external dependency
			if err := r.cleanupRelatedNamespaces(ctx, namespaceClass); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried.
				return ctrl.Result{}, err
			}

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(namespaceClass, NamespaceClassFinalizer)
			if err := r.Update(ctx, namespaceClass); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *NamespaceClassReconciler) cleanupRelatedNamespaces(ctx context.Context, ncl *qiujian16githubcomv1.NamespaceClass) error {
	logger := logf.FromContext(ctx)

	// List all namespaces with the NamespaceClassLabelKey label matching this namespaceclass
	namespaceList := &corev1.NamespaceList{}
	if err := r.Client.List(ctx, namespaceList, client.MatchingLabels{
		NamespaceClassLabelKey: ncl.Name,
	}); err != nil {
		return fmt.Errorf("failed to list namespaces for namespaceclass %s: %w", ncl.Name, err)
	}

	var errs []error

	// For each namespace, delete the resources defined in the namespaceclass spec
	for _, ns := range namespaceList.Items {
		logger.Info("cleaning up resources in namespace", "namespace", ns.Name, "namespaceclass", ncl.Name)

		nsErrs := r.deleteManifestsFromNamespace(ctx, ncl.Spec.Policies.Manifests, ns.Name)

		if len(nsErrs) == 0 {
			// All resources deleted successfully — remove the related resources annotation
			if err := r.removeRelatedResourcesAnnotation(ctx, ns.Name); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove annotation from namespace %s: %w", ns.Name, err))
			}
		} else {
			errs = append(errs, nsErrs...)
		}
	}

	return errors.Join(errs...)
}

// deleteManifestsFromNamespace attempts to delete all manifests from the given namespace.
// It continues on error and returns all errors encountered.
func (r *NamespaceClassReconciler) deleteManifestsFromNamespace(ctx context.Context, manifests []qiujian16githubcomv1.Manifest, namespace string) []error {
	logger := logf.FromContext(ctx)
	var errs []error

	for _, manifest := range manifests {
		if err := r.deleteManifestFromNamespace(ctx, manifest, namespace); err != nil {
			logger.Error(err, "failed to delete manifest from namespace", "namespace", namespace)
			errs = append(errs, err)
		}
	}

	return errs
}

// deleteManifestFromNamespace parses a raw manifest and deletes the corresponding
// resource from the specified namespace using the dynamic client.
func (r *NamespaceClassReconciler) deleteManifestFromNamespace(ctx context.Context, manifest qiujian16githubcomv1.Manifest, namespace string) error {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(manifest.Raw); err != nil {
		return fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	// Override the namespace on the object (in case the manifest doesn't specify it)
	obj.SetNamespace(namespace)

	return dynamicDelete(ctx, r.DynamicClient, r.RESTMapper, obj.GroupVersionKind(), namespace, obj.GetName())
}

// removeRelatedResourcesAnnotation removes the NamespaceClassRelatedResourcesAnnotationKey
// annotation from the specified namespace.
func (r *NamespaceClassReconciler) removeRelatedResourcesAnnotation(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		return fmt.Errorf("failed to get namespace %s: %w", namespace, err)
	}

	annotations := ns.GetAnnotations()
	if annotations == nil {
		return nil
	}

	if _, exists := annotations[NamespaceClassRelatedResourcesAnnotationKey]; !exists {
		return nil
	}

	delete(annotations, NamespaceClassRelatedResourcesAnnotationKey)
	ns.SetAnnotations(annotations)

	if err := r.Client.Update(ctx, ns); err != nil {
		return fmt.Errorf("failed to update namespace %s: %w", namespace, err)
	}

	logf.FromContext(ctx).Info("removed related resources annotation from namespace", "namespace", namespace)
	return nil
}

// dynamicDelete deletes a resource identified by GVK, namespace, and name
// using the provided dynamic client and REST mapper. It ignores NotFound errors.
func dynamicDelete(ctx context.Context, dynamicClient dynamic.Interface, restMapper meta.RESTMapper, gvk schema.GroupVersionKind, namespace, name string) error {
	logger := logf.FromContext(ctx)

	mapping, err := restMapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to find REST mapping for %s: %w", gvk, err)
	}

	resource := dynamicClient.Resource(mapping.Resource).Namespace(namespace)
	if err := resource.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete %s %s/%s: %w", gvk.Kind, namespace, name, err)
		}
		logger.V(1).Info("resource already deleted", "kind", gvk.Kind, "namespace", namespace, "name", name)
		return nil
	}

	logger.Info("deleted resource from namespace", "kind", gvk.Kind, "namespace", namespace, "name", name)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&qiujian16githubcomv1.NamespaceClass{}).
		Named("namespaceclass").
		Complete(r)
}
