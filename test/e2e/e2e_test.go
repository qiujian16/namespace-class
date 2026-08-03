//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/qiujian16/namespace-class/test/utils"
)

// namespace where the project is deployed in
const namespace = "namespace-class-system"

// serviceAccountName created for the project
const serviceAccountName = "namespace-class-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "namespace-class-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "namespace-class-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=namespace-class-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			By("waiting for the webhook service endpoints to be ready")
			verifyWebhookEndpointsReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpointslices.discovery.k8s.io", "-n", namespace,
					"-l", "kubernetes.io/service-name=namespace-class-webhook-service",
					"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{end}{end}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Webhook endpoints should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Webhook endpoints not yet ready")
			}
			Eventually(verifyWebhookEndpointsReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying the validating webhook server is ready")
			verifyValidatingWebhookReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "validatingwebhookconfigurations.admissionregistration.k8s.io",
					"namespace-class-validating-webhook-configuration",
					"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "ValidatingWebhookConfiguration should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Validating webhook CA bundle not yet injected")
			}
			Eventually(verifyValidatingWebhookReady, 3*time.Minute, time.Second).Should(Succeed())

			By("waiting additional time for webhook server to stabilize")
			time.Sleep(5 * time.Second)

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("should provisioned cert-manager", func() {
			By("validating that cert-manager has the certificate Secret")
			verifyCertManager := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "secrets", "webhook-server-cert", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyCertManager).Should(Succeed())
		})

		It("should have CA injection for validating webhooks", func() {
			By("checking CA injection for validating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"validatingwebhookconfigurations.admissionregistration.k8s.io",
					"namespace-class-validating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				vwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(vwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("NamespaceClass Workflow", func() {
		const (
			testNS       = "e2e-test-ns"
			nsClassName  = "e2e-nsclass"
			nsClassName2 = "e2e-nsclass-2"
			labelKey     = "namespaceclass.akuity.io/name"
		)

		// Manifests for different resource kinds used across tests.
		cmManifest := func(name string) string {
			return fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s"}}`, name)
		}
		saManifest := func(name string) string {
			return fmt.Sprintf(`{"apiVersion":"v1","kind":"ServiceAccount","metadata":{"name":"%s"}}`, name)
		}
		npManifest := func(name string) string {
			return fmt.Sprintf(`{"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"%s"},"spec":{"podSelector":{},"policyTypes":["Ingress"]}}`, name)
		}

		// kubectl helpers
		kubectl := func(args ...string) (string, error) {
			cmd := exec.Command("kubectl", args...)
			return utils.Run(cmd)
		}

		kubectlApply := func(manifest string) {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
		}

		// resourceExists waits for a resource to exist in the test namespace.
		resourceExists := func(kind, name string) {
			Eventually(func() string {
				out, _ := kubectl("get", kind, name, "-n", testNS, "-o", "jsonpath={.metadata.name}")
				return out
			}, "30s", "1s").Should(Equal(name))
		}

		// resourceGone waits for a resource to be deleted from the test namespace.
		resourceGone := func(kind, name string) {
			Eventually(func() string {
				out, _ := kubectl("get", kind, name, "-n", testNS, "--ignore-not-found", "-o", "jsonpath={.metadata.name}")
				return out
			}, "30s", "1s").Should(BeEmpty())
		}

		cleanupE2E := func() {
			// Remove namespace finalizer if present (via kubectl patch)
			_, _ = kubectl("patch", "namespace", testNS, "-p",
				`{"metadata":{"finalizers":[]}}`, "--type=merge")
			_, _ = kubectl("delete", "namespace", testNS, "--ignore-not-found", "--timeout=30s")

			// Remove namespaceclass finalizers
			for _, name := range []string{nsClassName, nsClassName2} {
				_, _ = kubectl("patch", "namespaceclass", name, "-p",
					`{"metadata":{"finalizers":[]}}`, "--type=merge")
				_, _ = kubectl("delete", "namespaceclass", name, "--ignore-not-found", "--timeout=30s")
			}
		}

		BeforeEach(func() {
			cleanupE2E()
		})

		AfterEach(func() {
			cleanupE2E()
		})

		// 1. Create NamespaceClass and label namespace
		It("should apply mixed resources when a namespace is labeled", func() {
			By("creating a NamespaceClass with ConfigMap, ServiceAccount, and NetworkPolicy")
			nsClass := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[
					%s,
					%s,
					%s
				]}}
			}`, nsClassName,
				cmManifest("e2e-cm"),
				saManifest("e2e-sa"),
				npManifest("e2e-np"),
			)
			kubectlApply(nsClass)

			By("creating a namespace with the NamespaceClass label")
			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName, "--overwrite")
			Expect(err).NotTo(HaveOccurred())

			By("waiting for resources to be created")
			resourceExists("configmap", "e2e-cm")
			resourceExists("serviceaccount", "e2e-sa")
			resourceExists("networkpolicy", "e2e-np")
		})

		// 2. Update NamespaceClass
		It("should add and remove mixed resources when NamespaceClass is updated", func() {
			By("creating a NamespaceClass with a ConfigMap and ServiceAccount")
			nsClass := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-a"), saManifest("e2e-sa-a"))
			kubectlApply(nsClass)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-a")
			resourceExists("serviceaccount", "e2e-sa-a")

			By("updating the NamespaceClass to replace cm-a with cm-b and add NetworkPolicy")
			nsClass = fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-b"), saManifest("e2e-sa-a"), npManifest("e2e-np"))
			kubectlApply(nsClass)

			By("verifying stale resources are removed and new ones are created")
			resourceGone("configmap", "e2e-cm-a")
			resourceExists("configmap", "e2e-cm-b")
			resourceExists("networkpolicy", "e2e-np")
		})

		// 3. Delete NamespaceClass
		It("should clean up mixed resources when NamespaceClass is deleted", func() {
			By("creating a NamespaceClass with ConfigMap and ServiceAccount")
			nsClass := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-a"), saManifest("e2e-sa-a"))
			kubectlApply(nsClass)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-a")
			resourceExists("serviceaccount", "e2e-sa-a")

			By("deleting the NamespaceClass")
			_, err = kubectl("delete", "namespaceclass", nsClassName)
			Expect(err).NotTo(HaveOccurred())

			By("verifying resources are cleaned up")
			resourceGone("configmap", "e2e-cm-a")
			resourceGone("serviceaccount", "e2e-sa-a")
		})

		// 4. Label namespace to another NamespaceClass
		It("should switch mixed resources when relabeling namespace", func() {
			By("creating two NamespaceClasses")
			nsClass1 := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-first"), saManifest("e2e-sa-first"))
			kubectlApply(nsClass1)

			nsClass2 := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName2, cmManifest("e2e-cm-second"), npManifest("e2e-np-second"))
			kubectlApply(nsClass2)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-first")
			resourceExists("serviceaccount", "e2e-sa-first")

			By("relabeling to the second NamespaceClass")
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName2, "--overwrite")
			Expect(err).NotTo(HaveOccurred())

			By("verifying first NC resources are removed and second NC resources are applied")
			resourceGone("configmap", "e2e-cm-first")
			resourceGone("serviceaccount", "e2e-sa-first")
			resourceExists("configmap", "e2e-cm-second")
			resourceExists("networkpolicy", "e2e-np-second")

			By("verifying the first NamespaceClass still exists (was not deleted)")
			out, err := kubectl("get", "namespaceclass", nsClassName, "-o", "jsonpath={.metadata.name}")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal(nsClassName))
		})

		// 4b. Delete the old NamespaceClass after switching — the namespace should be unaffected.
		It("should not affect namespace when old NamespaceClass is deleted after relabel", func() {
			By("creating two NamespaceClasses")
			nsClass1 := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-old"), saManifest("e2e-sa-old"))
			kubectlApply(nsClass1)

			nsClass2 := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName2, cmManifest("e2e-cm-new"), npManifest("e2e-np-new"))
			kubectlApply(nsClass2)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-old")
			resourceExists("serviceaccount", "e2e-sa-old")

			By("relabeling to the second NamespaceClass")
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName2, "--overwrite")
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-new")
			resourceExists("networkpolicy", "e2e-np-new")

			By("deleting the first NamespaceClass")
			_, err = kubectl("delete", "namespaceclass", nsClassName)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the second NamespaceClass resources are unaffected")
			Consistently(func() string {
				out, _ := kubectl("get", "configmap", "e2e-cm-new", "-n", testNS, "-o", "jsonpath={.metadata.name}")
				return out
			}, "10s", "1s").Should(Equal("e2e-cm-new"))
		})

		// 5. Remove label from namespace
		It("should clean up mixed resources when the label is removed", func() {
			nsClass := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-a"), npManifest("e2e-np-a"))
			kubectlApply(nsClass)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-a")
			resourceExists("networkpolicy", "e2e-np-a")

			By("removing the NamespaceClass label")
			_, err = kubectl("label", "namespace", testNS, labelKey+"-")
			Expect(err).NotTo(HaveOccurred())

			By("verifying resources are cleaned up")
			resourceGone("configmap", "e2e-cm-a")
			resourceGone("networkpolicy", "e2e-np-a")
		})

		// 6. Delete namespace
		It("should clean up mixed resources when namespace is deleted", func() {
			nsClass := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[%s,%s]}}
			}`, nsClassName, cmManifest("e2e-cm-a"), saManifest("e2e-sa-a"))
			kubectlApply(nsClass)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("configmap", "e2e-cm-a")
			resourceExists("serviceaccount", "e2e-sa-a")

			By("deleting the namespace")
			_, err = kubectl("delete", "namespace", testNS, "--timeout=60s")
			Expect(err).NotTo(HaveOccurred())

			By("verifying the namespace is deleted")
			Eventually(func() string {
				out, _ := kubectl("get", "namespace", testNS, "--ignore-not-found", "-o", "jsonpath={.metadata.name}")
				return out
			}, "60s", "1s").Should(BeEmpty())
		})

		// 7. External modification of managed resource
		It("should revert external modifications to managed resources", func() {
			nsClass := fmt.Sprintf(`{
				"apiVersion":"qiujian16.github.com.qiujian16.github.com/v1",
				"kind":"NamespaceClass",
				"metadata":{"name":"%s"},
				"spec":{"policies":{"manifests":[{
					"apiVersion":"v1",
					"kind":"ConfigMap",
					"metadata":{"name":"e2e-cm-managed"},
					"data":{"key":"original-value"}
				},%s]}}
			}`, nsClassName, saManifest("e2e-sa-managed"))
			kubectlApply(nsClass)

			_, err := kubectl("create", "namespace", testNS)
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl("label", "namespace", testNS, labelKey+"="+nsClassName)
			Expect(err).NotTo(HaveOccurred())

			resourceExists("serviceaccount", "e2e-sa-managed")
			Eventually(func() string {
				out, _ := kubectl("get", "configmap", "e2e-cm-managed", "-n", testNS, "-o", "jsonpath={.data.key}")
				return out
			}, "30s", "1s").Should(Equal("original-value"))

			By("externally modifying the ConfigMap")
			_, err = kubectl("patch", "configmap", "e2e-cm-managed", "-n", testNS, "-p",
				`{"data":{"key":"modified-externally"}}`)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the controller reverts the change")
			Eventually(func() string {
				out, _ := kubectl("get", "configmap", "e2e-cm-managed", "-n", testNS, "-o", "jsonpath={.data.key}")
				return out
			}, "30s", "1s").Should(Equal("original-value"))
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
