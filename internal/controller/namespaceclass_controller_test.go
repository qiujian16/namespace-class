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

var _ = Describe("NamespaceClass Controller", func() {
	ctx := context.Background()

	var namespaceClassName string
	var testNamespace string

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

	newReconciler := func() *NamespaceClassReconciler {
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

	BeforeEach(func() {
		namespaceClassName = "test-nsclass-" + randomSuffix()
		testNamespace = "test-ns-" + randomSuffix()
	})

	AfterEach(func() {
		// Clean up NamespaceClass
		ncl := &qiujian16githubcomv1.NamespaceClass{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: namespaceClassName}, ncl); err == nil {
			if controllerutil.ContainsFinalizer(ncl, NamespaceClassFinalizer) {
				controllerutil.RemoveFinalizer(ncl, NamespaceClassFinalizer)
				_ = k8sClient.Update(ctx, ncl)
			}
			if ncl.DeletionTimestamp.IsZero() {
				_ = k8sClient.Delete(ctx, ncl)
			}
		}
	})

	Context("When reconciling a NamespaceClass", func() {
		It("should add a finalizer to a new NamespaceClass", func() {
			ncl := createNamespaceClass(namespaceClassName)

			By("reconciling the NamespaceClass")
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ncl.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the finalizer was added")
			updated := &qiujian16githubcomv1.NamespaceClass{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ncl.Name}, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, NamespaceClassFinalizer)).To(BeTrue())
		})

		It("should clean up applied resources and remove finalizer on deletion", func() {
			ncl := createNamespaceClass(namespaceClassName,
				configMapManifest("cm-a"),
				configMapManifest("cm-b"),
			)

			By("reconciling to add finalizer")
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ncl.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			// Apply resources to a namespace so the annotation exists.
			createNamespace(testNamespace, namespaceClassName)
			nsReconciler := &NamespaceReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				DynamicClient: dynamicClient,
				RESTMapper:    restMapper,
			}
			_, err = nsReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: testNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify ConfigMaps exist
			cmA := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "cm-a", Namespace: testNamespace}, cmA)
			Expect(err).NotTo(HaveOccurred())
			cmB := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "cm-b", Namespace: testNamespace}, cmB)
			Expect(err).NotTo(HaveOccurred())

			By("marking the NamespaceClass for deletion")
			Expect(k8sClient.Delete(ctx, ncl)).To(Succeed())

			Eventually(func() bool {
				d := &qiujian16githubcomv1.NamespaceClass{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: ncl.Name}, d)
				return !d.DeletionTimestamp.IsZero()
			}, "5s", "100ms").Should(BeTrue())

			By("reconciling to trigger cleanup")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ncl.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying ConfigMaps are deleted")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "cm-a", Namespace: testNamespace}, cmA)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "cm-b", Namespace: testNamespace}, cmB)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying the finalizer was removed")
			final := &qiujian16githubcomv1.NamespaceClass{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: ncl.Name}, final)
			if err == nil {
				Expect(controllerutil.ContainsFinalizer(final, NamespaceClassFinalizer)).To(BeFalse())
			} else {
				Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}
		})

		It("should not block deletion when there are no related namespaces", func() {
			ncl := createNamespaceClass(namespaceClassName)

			By("reconciling to add finalizer")
			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ncl.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			By("marking the NamespaceClass for deletion")
			Expect(k8sClient.Delete(ctx, ncl)).To(Succeed())

			Eventually(func() bool {
				d := &qiujian16githubcomv1.NamespaceClass{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: ncl.Name}, d)
				return !d.DeletionTimestamp.IsZero()
			}, "5s", "100ms").Should(BeTrue())

			By("reconciling to trigger cleanup")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ncl.Name},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying finalizer was removed")
			final := &qiujian16githubcomv1.NamespaceClass{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: ncl.Name}, final)
			if err == nil {
				Expect(controllerutil.ContainsFinalizer(final, NamespaceClassFinalizer)).To(BeFalse())
			} else {
				Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}
		})
	})
})
