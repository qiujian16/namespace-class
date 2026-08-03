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

package v1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qiujian16githubcomv1 "github.com/qiujian16/namespace-class/api/v1"
)

// nolint:unused
// log is for logging in this package.
var namespaceclasslog = logf.Log.WithName("namespaceclass-resource")

// SetupNamespaceClassWebhookWithManager registers the webhook for NamespaceClass in the manager.
func SetupNamespaceClassWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &qiujian16githubcomv1.NamespaceClass{}).
		WithValidator(&NamespaceClassCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-qiujian16-github-com-qiujian16-github-com-v1-namespaceclass,mutating=false,failurePolicy=fail,sideEffects=None,groups=qiujian16.github.com.qiujian16.github.com,resources=namespaceclasses,verbs=create;update,versions=v1,name=vnamespaceclass-v1.kb.io,admissionReviewVersions=v1

// NamespaceClassCustomValidator struct is responsible for validating the NamespaceClass resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NamespaceClassCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassCustomValidator) ValidateCreate(_ context.Context, obj *qiujian16githubcomv1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon creation", "name", obj.GetName())
	return nil, validateManifests(obj.Spec.Policies.Manifests)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *qiujian16githubcomv1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon update", "name", newObj.GetName())
	return nil, validateManifests(newObj.Spec.Policies.Manifests)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassCustomValidator) ValidateDelete(_ context.Context, obj *qiujian16githubcomv1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon deletion", "name", obj.GetName())
	return nil, nil
}

// validateManifests checks for duplicate resources in the manifest list.
// apiVersion/kind/name/namespace are already validated at the CRD level
// via +kubebuilder:validation:EmbeddedResource.
func validateManifests(manifests []qiujian16githubcomv1.Manifest) error {
	seen := make(map[string]bool, len(manifests))

	for i, m := range manifests {
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(m.Raw); err != nil {
			return fmt.Errorf("manifest[%d]: failed to decode: %w", i, err)
		}

		gvk := obj.GroupVersionKind()

		// Namespace resources are not allowed — the controller targets namespaces,
		// and allowing them would create circular dependencies.
		if gvk.Group == "" && gvk.Version == "v1" && gvk.Kind == "Namespace" {
			return fmt.Errorf("manifest[%d]: Namespace resources are not allowed in NamespaceClass", i)
		}

		key := fmt.Sprintf("%s/%s/%s", gvk.GroupVersion().String(), gvk.Kind, obj.GetName())
		if seen[key] {
			return fmt.Errorf("manifest[%d] (%s/%s): duplicate resource", i, gvk.Kind, obj.GetName())
		}
		seen[key] = true
	}

	return nil
}
