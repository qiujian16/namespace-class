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
	"fmt"
	"math/rand"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	qiujian16githubcomv1 "github.com/qiujian16/namespace-class/api/v1"
)

var _ = Describe("Namespace Controller", func() {
	ctx := context.Background()

	// Unique names per test so no cross-test cleanup is needed.
	var testNS string
	var nsClassName1, nsClassName2 string

	// -----------------------------------------------------------------------
	// Helpers
	// -----------------------------------------------------------------------

	randomSuffix := func() string {
		return fmt.Sprintf("%08x", rand.Uint32())
	}

	configMapGVK := corev1.SchemeGroupVersion.WithKind("ConfigMap")
	configMapManifest := func(name string) qiujian16githubcomv1.Manifest {
		raw, _ := json.Marshal(&corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: configMapGVK.GroupVersion().String(), Kind: configMapGVK.Kind},
			ObjectMeta: metav1.ObjectMeta{Name: name},
		})
		return qiujian16githubcomv1.Manifest{RawExtension: runtime.RawExtension{Raw: raw}}
	}

	configMapManifestWithData := func(name string, data map[string]string) qiujian16githubcomv1.Manifest {
		raw, _ := json.Marshal(&corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: configMapGVK.GroupVersion().String(), Kind: configMapGVK.Kind},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Data:       data,
		})
		return qiujian16githubcomv1.Manifest{RawExtension: runtime.RawExtension{Raw: raw}}
	}

	newNSReconciler := func() *NamespaceReconciler {
		return &NamespaceReconciler{
			Client:        k8sClient,
			Scheme:        k8sClient.Scheme(),
			DynamicClient: dynamicClient,
			RESTMapper:    restMapper,
		}
	}

	newNCReconciler := func() *NamespaceClassReconciler {
		return &NamespaceClassReconciler{
			Client:        k8sClient,
			Scheme:        k8sClient.Scheme(),
			KubeClient:    kubeClient,
			DynamicClient: dynamicClient,
			RESTMapper:    restMapper,
		}
	}

	createNamespaceClass := func(name string, manifests ...qiujian16githubcomv1.Manifest) *qiujian16githubcomv1.NamespaceClass {
		ncl := &qiujian16githubcomv1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: qiujian16githubcomv1.NamespaceClassSpec{
				Policies: qiujian16githubcomv1.ManifestsTemplate{Manifests: manifests},
			},
		}
		Expect(k8sClient.Create(ctx, ncl)).To(Succeed())
		return ncl
	}

	createNamespace := func(name, nsClassName string) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{NamespaceClassLabelKey: nsClassName},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	}

	reconcileNamespace := func(name string) {
		_, err := newNSReconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	getConfigMap := func(name, namespace string) (*corev1.ConfigMap, error) {
		cm := &corev1.ConfigMap{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cm)
		return cm, err
	}

	getAnnotation := func(namespace string) []relatedResource {
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)).To(Succeed())
		return parseRelatedResourcesAnnotation(ns)
	}

	BeforeEach(func() {
		testNS = "test-ns-" + randomSuffix()
		nsClassName1 = "nc1-" + randomSuffix()
		nsClassName2 = "nc2-" + randomSuffix()
	})

	AfterEach(func() {
		// Force-delete NamespaceClasses by stripping finalizers.
		for _, name := range []string{nsClassName1, nsClassName2} {
			ncl := &qiujian16githubcomv1.NamespaceClass{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, ncl); err != nil {
				continue
			}
			if controllerutil.ContainsFinalizer(ncl, NamespaceClassFinalizer) {
				controllerutil.RemoveFinalizer(ncl, NamespaceClassFinalizer)
				_ = k8sClient.Update(ctx, ncl)
			}
			if ncl.DeletionTimestamp.IsZero() {
				_ = k8sClient.Delete(ctx, ncl)
			}
		}
	})

	// -----------------------------------------------------------------------
	// Scenarios
	// -----------------------------------------------------------------------

	// 1. Create NamespaceClass and then label a namespace
	It("should apply ConfigMaps when a namespace is labeled with a NamespaceClass", func() {
		By("creating a NamespaceClass with ConfigMap manifests")
		createNamespaceClass(nsClassName1,
			configMapManifest("cm-one"),
			configMapManifest("cm-two"),
		)

		By("labeling the namespace to reference the NamespaceClass")
		createNamespace(testNS, nsClassName1)

		By("reconciling the namespace")
		reconcileNamespace(testNS)

		By("verifying ConfigMaps are created")
		cm1, err := getConfigMap("cm-one", testNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(cm1.Name).To(Equal("cm-one"))

		cm2, err := getConfigMap("cm-two", testNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(cm2.Name).To(Equal("cm-two"))

		By("verifying the annotation records the applied resources with status")
		resources := getAnnotation(testNS)
		Expect(resources).To(HaveLen(2))
		for _, r := range resources {
			Expect(r.Status.Type).To(Equal("Applied"))
			Expect(r.Status.Status).To(Equal(metav1.ConditionTrue))
			Expect(r.Status.Reason).To(Equal("ResourceApplied"))
		}
	})

	// 2. Update NamespaceClass (add and remove manifests)
	It("should add and remove resources when the NamespaceClass is updated", func() {
		By("creating a NamespaceClass with one ConfigMap")
		ncl := createNamespaceClass(nsClassName1, configMapManifest("cm-one"))
		createNamespace(testNS, nsClassName1)
		reconcileNamespace(testNS)

		By("adding a second ConfigMap to the NamespaceClass")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsClassName1}, ncl)).To(Succeed())
		ncl.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{
			configMapManifest("cm-one"),
			configMapManifest("cm-two"),
		}
		Expect(k8sClient.Update(ctx, ncl)).To(Succeed())
		reconcileNamespace(testNS)

		By("verifying both ConfigMaps exist")
		_, err := getConfigMap("cm-one", testNS)
		Expect(err).NotTo(HaveOccurred())
		_, err = getConfigMap("cm-two", testNS)
		Expect(err).NotTo(HaveOccurred())

		By("removing cm-one from the NamespaceClass")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsClassName1}, ncl)).To(Succeed())
		ncl.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{configMapManifest("cm-two")}
		Expect(k8sClient.Update(ctx, ncl)).To(Succeed())
		reconcileNamespace(testNS)

		By("verifying cm-one is deleted and cm-two remains")
		_, err = getConfigMap("cm-one", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getConfigMap("cm-two", testNS)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the annotation only contains cm-two with success status")
		resources := getAnnotation(testNS)
		Expect(resources).To(HaveLen(1))
		Expect(resources[0].Name).To(Equal("cm-two"))
		Expect(resources[0].Status.Type).To(Equal("Applied"))
		Expect(resources[0].Status.Status).To(Equal(metav1.ConditionTrue))
		Expect(resources[0].Status.Reason).To(Equal("ResourceApplied"))
	})

	// 3. Delete NamespaceClass
	It("should clean up all resources when the NamespaceClass is deleted", func() {
		By("creating a NamespaceClass with ConfigMaps")
		ncl := createNamespaceClass(nsClassName1,
			configMapManifest("cm-one"),
			configMapManifest("cm-two"),
		)
		createNamespace(testNS, nsClassName1)
		reconcileNamespace(testNS)

		By("deleting the NamespaceClass")
		Expect(k8sClient.Delete(ctx, ncl)).To(Succeed())

		// Wait for deletion timestamp (or full deletion if no finalizer).
		Eventually(func() bool {
			n := &qiujian16githubcomv1.NamespaceClass{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: nsClassName1}, n)
			if apierrors.IsNotFound(err) {
				return true
			}
			return err == nil && !n.DeletionTimestamp.IsZero()
		}, "5s", "100ms").Should(BeTrue())

		// Reconcile the NamespaceClass to trigger cleanup of related namespaces
		_, nsClassErr := newNCReconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: nsClassName1},
		})
		Expect(nsClassErr).NotTo(HaveOccurred())

		By("reconciling the namespace — NamespaceClass is gone")
		reconcileNamespace(testNS)

		By("verifying ConfigMaps are deleted")
		_, err := getConfigMap("cm-one", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getConfigMap("cm-two", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		By("verifying annotation is removed")
		Expect(getAnnotation(testNS)).To(BeEmpty())
	})

	// 4. Label namespace to another NamespaceClass
	It("should switch resources when the namespace is relabeled to another NamespaceClass", func() {
		By("creating two NamespaceClasses")
		createNamespaceClass(nsClassName1, configMapManifest("cm-from-first"))
		createNamespaceClass(nsClassName2, configMapManifest("cm-from-second"))

		By("labeling namespace to the first NamespaceClass")
		createNamespace(testNS, nsClassName1)
		reconcileNamespace(testNS)
		_, err := getConfigMap("cm-from-first", testNS)
		Expect(err).NotTo(HaveOccurred())

		By("relabeling the namespace to the second NamespaceClass")
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, ns)).To(Succeed())
		ns.Labels[NamespaceClassLabelKey] = nsClassName2
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		reconcileNamespace(testNS)

		By("verifying first NamespaceClass resources are removed")
		_, err = getConfigMap("cm-from-first", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		By("verifying second NamespaceClass resources are applied")
		_, err = getConfigMap("cm-from-second", testNS)
		Expect(err).NotTo(HaveOccurred())

		By("verifying annotation reflects only the second NamespaceClass with success status")
		resources := getAnnotation(testNS)
		Expect(resources).To(HaveLen(1))
		Expect(resources[0].Name).To(Equal("cm-from-second"))
		Expect(resources[0].Status.Type).To(Equal("Applied"))
		Expect(resources[0].Status.Status).To(Equal(metav1.ConditionTrue))
		Expect(resources[0].Status.Reason).To(Equal("ResourceApplied"))
	})

	// 5. Remove label from namespace
	It("should clean up resources and remove the finalizer when the label is removed", func() {
		By("creating a NamespaceClass and labeling a namespace")
		createNamespaceClass(nsClassName1, configMapManifest("cm-one"))
		createNamespace(testNS, nsClassName1)
		reconcileNamespace(testNS)

		By("removing the NamespaceClass label from the namespace")
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, ns)).To(Succeed())
		delete(ns.Labels, NamespaceClassLabelKey)
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		reconcileNamespace(testNS)

		By("verifying ConfigMaps are deleted")
		_, err := getConfigMap("cm-one", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		By("verifying the annotation is removed")
		Expect(getAnnotation(testNS)).To(BeEmpty())

		By("verifying the finalizer is removed")
		updated := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, updated)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(updated, NamespaceFinalizer)).To(BeFalse())
	})

	// 6. Delete namespace
	It("should clean up resources and remove finalizer when the namespace is deleted", func() {
		By("creating a NamespaceClass and labeling a namespace")
		createNamespaceClass(nsClassName1,
			configMapManifest("cm-one"),
			configMapManifest("cm-two"),
		)
		createNamespace(testNS, nsClassName1)
		reconcileNamespace(testNS)

		By("verifying ConfigMaps exist")
		_, err := getConfigMap("cm-one", testNS)
		Expect(err).NotTo(HaveOccurred())

		By("deleting the namespace")
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, ns)).To(Succeed())
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())

		// Wait for deletion timestamp (or full deletion).
		Eventually(func() bool {
			n := &corev1.Namespace{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, n)
			if apierrors.IsNotFound(err) {
				return true
			}
			return err == nil && !n.DeletionTimestamp.IsZero()
		}, "5s", "100ms").Should(BeTrue())

		By("reconciling the namespace while it is being deleted")
		reconcileNamespace(testNS)

		By("verifying ConfigMaps are deleted")
		_, err = getConfigMap("cm-one", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getConfigMap("cm-two", testNS)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		By("verifying the finalizer was removed")
		final := &corev1.Namespace{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, final)
		if err == nil {
			Expect(controllerutil.ContainsFinalizer(final, NamespaceFinalizer)).To(BeFalse())
		}
	})

	// 7. Update a resource (external modification → controller reverts)
	// 8. Error case — nonexistent resource type records apply failure in status.
	It("should record apply failure status for invalid resource types", func() {
		By("creating a NamespaceClass with a valid ConfigMap and an invalid resource")
		invalidManifest := qiujian16githubcomv1.Manifest{RawExtension: runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"invalid.example.com/v1","kind":"FakeThing","metadata":{"name":"bad-resource"}}`),
		}}
		createNamespaceClass(nsClassName1,
			configMapManifest("cm-valid"),
			invalidManifest,
		)
		createNamespace(testNS, nsClassName1)

		By("reconciling (expecting apply errors for the invalid manifest)")
		_, reconcileErr := newNSReconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: testNS},
		})
		Expect(reconcileErr).To(HaveOccurred())

		By("verifying the valid ConfigMap was created")
		_, err := getConfigMap("cm-valid", testNS)
		Expect(err).NotTo(HaveOccurred())

		By("checking the annotation has status for both resources")
		resources := getAnnotation(testNS)
		Expect(resources).To(HaveLen(2))

		for _, r := range resources {
			switch r.Name {
			case "cm-valid":
				Expect(r.Status.Type).To(Equal("Applied"))
				Expect(r.Status.Status).To(Equal(metav1.ConditionTrue))
				Expect(r.Status.Reason).To(Equal("ResourceApplied"))
			case "bad-resource":
				Expect(r.Status.Type).To(Equal("Applied"))
				Expect(r.Status.Status).To(Equal(metav1.ConditionFalse))
				Expect(r.Status.Reason).To(Equal("ResourceApplyFailed"))
				Expect(r.Status.Message).NotTo(BeEmpty())
			}
		}
	})

	It("should revert external modifications to managed resources", func() {
		By("creating a NamespaceClass with a ConfigMap")
		createNamespaceClass(nsClassName1,
			configMapManifestWithData("cm-managed", map[string]string{"key": "original-value"}),
		)
		createNamespace(testNS, nsClassName1)
		reconcileNamespace(testNS)

		By("verifying the ConfigMap exists with the original value")
		cm, err := getConfigMap("cm-managed", testNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(cm.Data).To(HaveKeyWithValue("key", "original-value"))

		By("externally modifying the ConfigMap")
		cm.Data["key"] = "modified-by-external-actor"
		Expect(k8sClient.Update(ctx, cm)).To(Succeed())

		// Verify the external modification took effect
		modified := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cm-managed", Namespace: testNS}, modified)).To(Succeed())
		Expect(modified.Data).To(HaveKeyWithValue("key", "modified-by-external-actor"))

		By("reconciling the namespace — should revert the change")
		reconcileNamespace(testNS)

		By("verifying the ConfigMap was reverted")
		reverted := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cm-managed", Namespace: testNS}, reverted)).To(Succeed())
		Expect(reverted.Data).To(HaveKeyWithValue("key", "original-value"))
	})
})
