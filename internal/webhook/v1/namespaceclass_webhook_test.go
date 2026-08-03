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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime"

	qiujian16githubcomv1 "github.com/qiujian16/namespace-class/api/v1"
)

var _ = Describe("NamespaceClass Webhook", func() {
	var (
		obj       *qiujian16githubcomv1.NamespaceClass
		validator NamespaceClassCustomValidator
	)

	configMapManifest := func(name string) qiujian16githubcomv1.Manifest {
		return qiujian16githubcomv1.Manifest{RawExtension: runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"` + name + `"}}`),
		}}
	}

	namespaceManifest := func(name string) qiujian16githubcomv1.Manifest {
		return qiujian16githubcomv1.Manifest{RawExtension: runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"` + name + `"}}`),
		}}
	}

	BeforeEach(func() {
		obj = &qiujian16githubcomv1.NamespaceClass{}
		validator = NamespaceClassCustomValidator{}
	})

	Context("When creating or updating NamespaceClass", func() {
		It("should admit a NamespaceClass with valid manifests", func() {
			obj.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{
				configMapManifest("cm-a"),
				configMapManifest("cm-b"),
			}
			_, err := validator.ValidateCreate(nil, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should admit an empty manifest list", func() {
			_, err := validator.ValidateCreate(nil, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject duplicate manifests", func() {
			obj.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{
				configMapManifest("cm-a"),
				configMapManifest("cm-b"),
				configMapManifest("cm-a"), // duplicate
			}
			_, err := validator.ValidateCreate(nil, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate"))
		})

		It("should reject Namespace resources", func() {
			obj.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{
				configMapManifest("cm-a"),
				namespaceManifest("some-ns"),
			}
			_, err := validator.ValidateCreate(nil, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Namespace"))
		})

		It("should reject manifests with invalid JSON", func() {
			obj.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{
				{RawExtension: runtime.RawExtension{Raw: []byte(`not-json`)}},
			}
			_, err := validator.ValidateCreate(nil, obj)
			Expect(err).To(HaveOccurred())
		})

		It("should validate updates with the same rules", func() {
			obj.Spec.Policies.Manifests = []qiujian16githubcomv1.Manifest{
				configMapManifest("cm-a"),
				configMapManifest("cm-a"), // duplicate
			}
			_, err := validator.ValidateUpdate(nil, &qiujian16githubcomv1.NamespaceClass{}, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate"))
		})
	})
})
