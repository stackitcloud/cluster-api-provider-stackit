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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/test/utils"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

// namespace where the project is deployed in
const namespace = "cluster-api-provider-stackit-system"

// serviceAccountName created for the project
const serviceAccountName = "cluster-api-provider-stackit-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "cluster-api-provider-stackit-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "cluster-api-provider-stackit-metrics-binding"

const stackitE2EMachineType = "c2i.2"

const defaultKubernetesVersion = "v1.33.12"

const cloudProviderStackitImageRepository = "ghcr.io/stackitcloud/cloud-provider-stackit/cloud-controller-manager"

var defaultCloudProviderStackitImages = map[string]string{
	"1.33": cloudProviderStackitImageRepository + ":v1.33.12",
	"1.34": cloudProviderStackitImageRepository + ":v1.34.8",
	"1.35": cloudProviderStackitImageRepository + ":v1.35.3",
	"1.36": cloudProviderStackitImageRepository + ":v1.36.0",
}

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("ensuring manager namespace exists")
		cmd := exec.Command("kubectl", "get", "ns", namespace)
		var err error
		if _, err := utils.Run(cmd); err != nil {
			cmd = exec.Command("kubectl", "create", "ns", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")
		}

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

		By("restarting the controller-manager to pick up the freshly loaded image")
		cmd = exec.Command("kubectl", "rollout", "restart", "deployment/cluster-api-provider-stackit-controller-manager", "-n", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to restart the controller-manager")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/cluster-api-provider-stackit-controller-manager", "-n", namespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "controller-manager did not roll out")
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
				"--clusterrole=cluster-api-provider-stackit-metrics-reader",
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
					"-l", "kubernetes.io/service-name=cluster-api-provider-stackit-webhook-service",
					"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{end}{end}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Webhook endpoints should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Webhook endpoints not yet ready")
			}
			Eventually(verifyWebhookEndpointsReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying the mutating webhook server is ready")
			verifyMutatingWebhookReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mutatingwebhookconfigurations.admissionregistration.k8s.io",
					"cluster-api-provider-stackit-mutating-webhook-configuration",
					"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "MutatingWebhookConfiguration should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Mutating webhook CA bundle not yet injected")
			}
			Eventually(verifyMutatingWebhookReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying the validating webhook server is ready")
			verifyValidatingWebhookReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "validatingwebhookconfigurations.admissionregistration.k8s.io",
					"cluster-api-provider-stackit-validating-webhook-configuration",
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

		It("should provision the webhook certificate Secret", func() {
			By("validating that cert-manager has the certificate Secret")
			verifyCertManager := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "secrets", "webhook-server-cert", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyCertManager).Should(Succeed())
		})

		It("should have CA injection for mutating webhooks", func() {
			By("checking CA injection for mutating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"mutatingwebhookconfigurations.admissionregistration.k8s.io",
					"cluster-api-provider-stackit-mutating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				mwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(mwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		It("should have CA injection for validating webhooks", func() {
			By("checking CA injection for validating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"validatingwebhookconfigurations.admissionregistration.k8s.io",
					"cluster-api-provider-stackit-validating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				vwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(vwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		It("should create and delete a real STACKIT VM for a workload Cluster Machine", func() {
			if os.Getenv("STACKIT_E2E_CREATE_VMS") != "true" {
				Skip("set STACKIT_E2E_CREATE_VMS=true to run the real STACKIT VM lifecycle e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			clusterName := fmt.Sprintf("stackit-e2e-%d", time.Now().Unix())
			machineName := clusterName + "-machine-0"
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying a workload Cluster and StackitMachine fixture")
			fixture := renderStackitVMFixture(clusterName, machineName, testID, cfg)
			fixturePath := writeTempManifest("stackit-vm-e2e-*.yaml", fixture)
			defer func() {
				cleanupStackitVMFixture(clusterName, machineName, cfg.Namespace)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT VM lifecycle fixture")

			By("waiting for the StackitCluster to validate credentials and network")
			Eventually(func(g Gomega) {
				output := kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", cfg.Namespace, "-o", "jsonpath={.status.ready}")
				g.Expect(output).To(Equal("true"))
			}, 10*time.Minute, 10*time.Second).Should(Succeed())

			By("waiting for the StackitMachine to provision a VM")
			var instanceID string
			Eventually(func(g Gomega) {
				ready := kubectlOutput(g, "get", "stackitmachine", machineName, "-n", cfg.Namespace, "-o", "jsonpath={.status.ready}")
				g.Expect(ready).To(Equal("true"))
				instanceID = kubectlOutput(g, "get", "stackitmachine", machineName, "-n", cfg.Namespace, "-o", "jsonpath={.status.instanceID}")
				g.Expect(instanceID).NotTo(BeEmpty())
			}, 25*time.Minute, 15*time.Second).Should(Succeed())

			By("verifying the VM exists in STACKIT")
			Eventually(func(g Gomega) {
				server, err := cloudClient.GetServer(ctx, instanceID)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(server.ID).To(Equal(instanceID))
			}, 5*time.Minute, 15*time.Second).Should(Succeed())

			By("deleting the StackitMachine to trigger VM cleanup")
			cmd = exec.Command("kubectl", "delete", "stackitmachine", machineName, "-n", cfg.Namespace, "--wait=true", "--timeout=20m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete StackitMachine")

			By("verifying the VM was deleted from STACKIT")
			Eventually(func(g Gomega) {
				_, err := cloudClient.GetServer(ctx, instanceID)
				g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected server %s to be deleted, got %v", instanceID, err)
			}, 20*time.Minute, 15*time.Second).Should(Succeed())

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 5*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should create and delete a 1 control-plane / 1 worker workload Cluster without STACKIT leaks", func() {
			if os.Getenv("STACKIT_E2E_CREATE_CLUSTER") != "true" {
				Skip("set STACKIT_E2E_CREATE_CLUSTER=true to run the real STACKIT Cluster lifecycle e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			clusterName := fmt.Sprintf("stackit-e2e-cluster-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying a 1 control-plane / 1 worker workload Cluster fixture")
			fixture := renderStackitClusterFixture(clusterName, testID, cfg)
			fixturePath := writeTempManifest("stackit-cluster-e2e-*.yaml", fixture)
			defer func() {
				cleanupStackitClusterFixture(clusterName, cfg.Namespace)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT Cluster lifecycle fixture")

			By("waiting for the StackitCluster to become ready")
			Eventually(func(g Gomega) {
				output := kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", cfg.Namespace, "-o", "jsonpath={.status.ready}")
				g.Expect(output).To(Equal("true"))
			}, 15*time.Minute, 10*time.Second).Should(Succeed())

			if cfg.BastionEnabled {
				By("verifying the bastion public IP and security group exist")
				Eventually(func(g Gomega) {
					output := kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", cfg.Namespace, "-o", "jsonpath={.status.bastion.publicIP}")
					g.Expect(output).NotTo(BeEmpty())
					publicIPs, err := cloudClient.ListPublicIPsByTags(ctx, stackitE2EBastionTags(testID))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(publicIPs).To(HaveLen(1))
					securityGroups, err := cloudClient.ListSecurityGroupsByTags(ctx, stackitE2EBastionTags(testID))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(securityGroups).To(HaveLen(1))
				}, 10*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("waiting for one control-plane and one worker StackitMachine to provision")
			var instanceIDs []string
			Eventually(func(g Gomega) {
				machines := stackitMachinesForTestID(g, cfg.Namespace, testID)
				g.Expect(machines).To(HaveLen(2))
				instanceIDs = instanceIDs[:0]
				for _, machine := range machines {
					g.Expect(machine.Status.Ready).To(BeTrue(), "StackitMachine %s is not ready", machine.Metadata.Name)
					g.Expect(machine.Status.InstanceID).NotTo(BeEmpty(), "StackitMachine %s has no instanceID", machine.Metadata.Name)
					instanceIDs = append(instanceIDs, machine.Status.InstanceID)
				}
			}, 45*time.Minute, 15*time.Second).Should(Succeed())

			if cfg.BastionEnabled {
				By("verifying the node SSH security group exists")
				Eventually(func(g Gomega) {
					securityGroups, err := cloudClient.ListSecurityGroupsByTags(ctx, stackitE2ENodeSSHTags(testID))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(securityGroups).To(HaveLen(1))
				}, 10*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("verifying both VMs exist in STACKIT")
			for _, instanceID := range instanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					server, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(server.ID).To(Equal(instanceID))
				}, 5*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("deleting the workload Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete workload Cluster")

			By("verifying Kubernetes resources for the workload Cluster are gone")
			Eventually(func(g Gomega) {
				output := kubectlOutput(g, "get", "cluster", clusterName, "-n", cfg.Namespace, "-o", "name", "--ignore-not-found")
				g.Expect(output).To(BeEmpty())
				output = kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", cfg.Namespace, "-o", "name", "--ignore-not-found")
				g.Expect(output).To(BeEmpty())
				output = kubectlOutput(g, "get", "machine,stackitmachine", "-n", cfg.Namespace,
					"-l", fmt.Sprintf("cluster.x-k8s.io/cluster-name=%s", clusterName), "-o", "name", "--ignore-not-found")
				g.Expect(output).To(BeEmpty())
				g.Expect(stackitMachinesForTestID(g, cfg.Namespace, testID)).To(BeEmpty())
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
				publicIPs, err := cloudClient.ListPublicIPsByTags(ctx, stackitE2EBastionTags(testID))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(publicIPs).To(BeEmpty())
				securityGroups, err := cloudClient.ListSecurityGroupsByTags(ctx, stackitE2EBastionTags(testID))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(securityGroups).To(BeEmpty())
				securityGroups, err = cloudClient.ListSecurityGroupsByTags(ctx, stackitE2ENodeSSHTags(testID))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(securityGroups).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should align StackitMachine, Machine, and Node providerIDs in a real workload Cluster", func() {
			if os.Getenv("STACKIT_E2E_NODE_REF") != "true" {
				Skip("set STACKIT_E2E_NODE_REF=true to run the real workload Cluster NodeRef e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			credentials := stackitCredentialsSecret(ctx, cfg.CredentialsSecretName, cfg.CredentialsSecretNS)
			serviceAccountJSON, ok := credentials["serviceaccount.json"]
			Expect(ok).To(BeTrue(), "credentials Secret is missing serviceaccount.json")
			clusterName := fmt.Sprintf("stackit-e2e-noderef-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			kubernetesVersion := envDefault("KUBERNETES_VERSION", defaultKubernetesVersion)
			validateSupportedKubernetesVersion(kubernetesVersion)
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying a real kubeadm workload Cluster fixture")
			fixture := renderStackitKubeadmClusterFixture(clusterName, testID, cfg, kubernetesVersion, serviceAccountJSON)
			fixturePath := writeTempManifest("stackit-noderef-e2e-*.yaml", fixture)
			var workloadKubeconfig string
			defer func() {
				cleanupStackitKubeadmClusterFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				if workloadKubeconfig != "" {
					_ = os.Remove(workloadKubeconfig)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT NodeRef fixture")

			waitForKubeadmWorkloadClusterReady(workloadReadinessOptions{
				Namespace:           cfg.Namespace,
				ClusterName:         clusterName,
				TestID:              testID,
				WantNodes:           2,
				WantMachines:        2,
				InstallCNI:          true,
				WaitForCCM:          true,
				ObservedInstanceIDs: &observedInstanceIDs,
			}, &workloadKubeconfig)

			By("deleting the workload Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete NodeRef workload Cluster")

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should create a topology workload Cluster with Ready Nodes", func() {
			if os.Getenv("STACKIT_E2E_TOPOLOGY_WORKLOAD") != "true" {
				Skip("set STACKIT_E2E_TOPOLOGY_WORKLOAD=true to run the real workload topology e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			credentials := stackitCredentialsSecret(ctx, cfg.CredentialsSecretName, cfg.CredentialsSecretNS)
			serviceAccountJSON, ok := credentials["serviceaccount.json"]
			Expect(ok).To(BeTrue(), "credentials Secret is missing serviceaccount.json")
			clusterName := fmt.Sprintf("stackit-e2e-topo-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			kubernetesVersion := envDefault("KUBERNETES_VERSION", defaultKubernetesVersion)
			validateSupportedKubernetesVersion(kubernetesVersion)
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying the STACKIT ClusterClass")
			clusterClass := renderTopologyClusterClassFixture()
			clusterClassPath := writeTempManifest("stackit-topology-clusterclass-e2e-*.yaml", clusterClass)
			cmd := exec.Command("kubectl", "apply", "-n", cfg.Namespace, "-f", clusterClassPath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT ClusterClass")

			By("applying a topology workload Cluster fixture")
			fixture := renderTopologyWorkloadClusterFixture(topologyWorkloadFixtureOptions{
				ClusterName:        clusterName,
				TestID:             testID,
				Config:             cfg,
				KubernetesVersion:  kubernetesVersion,
				ServiceAccountJSON: serviceAccountJSON,
			})
			fixturePath := writeTempManifest("stackit-topology-workload-e2e-*.yaml", fixture)
			var workloadKubeconfig string
			defer func() {
				cleanupStackitTopologyClusterFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				if workloadKubeconfig != "" {
					_ = os.Remove(workloadKubeconfig)
				}
				_ = os.Remove(fixturePath)
				_ = os.Remove(clusterClassPath)
			}()

			cmd = exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT topology workload fixture")

			waitForKubeadmWorkloadClusterReady(workloadReadinessOptions{
				Namespace:           cfg.Namespace,
				ClusterName:         clusterName,
				TestID:              testID,
				WantNodes:           2,
				WantMachines:        2,
				InstallCNI:          true,
				WaitForCCM:          true,
				Topology:            true,
				ObservedInstanceIDs: &observedInstanceIDs,
			}, &workloadKubeconfig)

			By("verifying topology template metadata is propagated to generated infrastructure resources")
			Eventually(func(g Gomega) {
				expectTopologyTemplateMetadata(g, cfg.Namespace, clusterName, testID)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			By("deleting the topology workload Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete topology workload Cluster")

			By("verifying topology CAPI and infrastructure resources are gone")
			Eventually(func(g Gomega) {
				g.Expect(kubectlOutput(g, "get", "machine", "-n", cfg.Namespace, "-l", "cluster.x-k8s.io/cluster-name="+clusterName, "-o", "name")).To(BeEmpty())
				g.Expect(kubectlOutput(g, "get", "stackitmachine", "-n", cfg.Namespace, "-l", "cluster.x-k8s.io/cluster-name="+clusterName, "-o", "name")).To(BeEmpty())
				g.Expect(kubectlOutput(g, "get", "stackitcluster", "-n", cfg.Namespace, "-l", "cluster.x-k8s.io/cluster-name="+clusterName, "-o", "name")).To(BeEmpty())
			}, 10*time.Minute, 15*time.Second).Should(Succeed())

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should scale a worker MachineDeployment up and down without STACKIT leaks", func() {
			if os.Getenv("STACKIT_E2E_SCALE_WORKERS") != "true" {
				Skip("set STACKIT_E2E_SCALE_WORKERS=true to run the real STACKIT MachineDeployment scale e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			clusterName := fmt.Sprintf("stackit-e2e-scale-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			machineDeploymentName := clusterName + "-md-0"
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying an infra-only worker MachineDeployment scale fixture")
			kubernetesVersion := envDefault("KUBERNETES_VERSION", defaultKubernetesVersion)
			validateSupportedKubernetesVersion(kubernetesVersion)
			fixture := renderStackitInfraOnlyMachineDeploymentFixture(clusterName, testID, cfg, kubernetesVersion)
			fixturePath := writeTempManifest("stackit-md-scale-e2e-*.yaml", fixture)
			defer func() {
				cleanupStackitMachineDeploymentScaleFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT MachineDeployment scale fixture")

			By("waiting for the StackitCluster to become ready")
			Eventually(func(g Gomega) {
				output := kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", cfg.Namespace, "-o", "jsonpath={.status.ready}")
				g.Expect(output).To(Equal("true"))
			}, 15*time.Minute, 10*time.Second).Should(Succeed())

			By("waiting for the initial worker Machine to provision")
			var initialInstanceIDs []string
			Eventually(func(g Gomega) {
				initialInstanceIDs = readyStackitMachineInstanceIDs(g, cfg.Namespace, testID, 1)
			}, 45*time.Minute, 15*time.Second).Should(Succeed())
			observedInstanceIDs = appendUnique(observedInstanceIDs, initialInstanceIDs...)
			Eventually(func(g Gomega) {
				expectCAPIMachinesWithProviderIDs(g, cfg.Namespace, clusterName, 1)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			By("scaling workers up to three replicas")
			cmd = exec.Command("kubectl", "scale", "machinedeployment", machineDeploymentName, "-n", cfg.Namespace, "--replicas=3")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to scale MachineDeployment up")

			var scaledUpInstanceIDs []string
			Eventually(func(g Gomega) {
				scaledUpInstanceIDs = readyStackitMachineInstanceIDs(g, cfg.Namespace, testID, 3)
			}, 45*time.Minute, 15*time.Second).Should(Succeed())
			observedInstanceIDs = appendUnique(observedInstanceIDs, scaledUpInstanceIDs...)
			Eventually(func(g Gomega) {
				expectCAPIMachinesWithProviderIDs(g, cfg.Namespace, clusterName, 3)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			for _, instanceID := range initialInstanceIDs {
				Expect(scaledUpInstanceIDs).To(ContainElement(instanceID))
			}

			By("verifying all scaled-up worker VMs exist in STACKIT")
			for _, instanceID := range scaledUpInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					server, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(server.ID).To(Equal(instanceID))
				}, 5*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("scaling workers back down to one replica")
			cmd = exec.Command("kubectl", "scale", "machinedeployment", machineDeploymentName, "-n", cfg.Namespace, "--replicas=1")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to scale MachineDeployment down")

			var remainingInstanceIDs []string
			Eventually(func(g Gomega) {
				remainingInstanceIDs = readyStackitMachineInstanceIDs(g, cfg.Namespace, testID, 1)
			}, 30*time.Minute, 15*time.Second).Should(Succeed())
			observedInstanceIDs = appendUnique(observedInstanceIDs, remainingInstanceIDs...)
			Eventually(func(g Gomega) {
				expectCAPIMachinesWithProviderIDs(g, cfg.Namespace, clusterName, 1)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			deletedInstanceIDs := difference(scaledUpInstanceIDs, remainingInstanceIDs)
			Expect(deletedInstanceIDs).To(HaveLen(2))

			By("verifying scaled-down worker VMs were deleted from STACKIT")
			for _, instanceID := range deletedInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					_, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected server %s to be deleted, got %v", instanceID, err)
				}, 20*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("deleting the scale test workload Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete scale test workload Cluster")

			By("verifying all observed worker VMs were deleted from STACKIT")
			for _, instanceID := range observedInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					_, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected server %s to be deleted, got %v", instanceID, err)
				}, 20*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should scale workload worker Nodes up and down without STACKIT leaks", func() {
			if os.Getenv("STACKIT_E2E_SCALE_WORKLOAD") != "true" {
				Skip("set STACKIT_E2E_SCALE_WORKLOAD=true to run the real workload MachineDeployment scale e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			credentials := stackitCredentialsSecret(ctx, cfg.CredentialsSecretName, cfg.CredentialsSecretNS)
			serviceAccountJSON, ok := credentials["serviceaccount.json"]
			Expect(ok).To(BeTrue(), "credentials Secret is missing serviceaccount.json")
			clusterName := fmt.Sprintf("stackit-e2e-wscale-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			machineDeploymentName := clusterName + "-md-0"
			kubernetesVersion := envDefault("KUBERNETES_VERSION", defaultKubernetesVersion)
			validateSupportedKubernetesVersion(kubernetesVersion)
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying a real kubeadm workload Cluster fixture for scale")
			fixture := renderKubeadmWorkloadClusterFixture(kubeadmWorkloadFixtureOptions{
				ClusterName:           clusterName,
				TestID:                testID,
				Config:                cfg,
				KubernetesVersion:     kubernetesVersion,
				ServiceAccountJSON:    serviceAccountJSON,
				ControlPlaneReplicas:  1,
				WorkerReplicas:        1,
				APIServerLoadBalancer: true,
				IncludeCCMAddon:       true,
			})
			fixturePath := writeTempManifest("stackit-scale-workload-e2e-*.yaml", fixture)
			var workloadKubeconfig string
			defer func() {
				cleanupStackitKubeadmClusterFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				if workloadKubeconfig != "" {
					_ = os.Remove(workloadKubeconfig)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT workload scale fixture")

			waitForKubeadmWorkloadClusterReady(workloadReadinessOptions{
				Namespace:           cfg.Namespace,
				ClusterName:         clusterName,
				TestID:              testID,
				WantNodes:           2,
				WantMachines:        2,
				InstallCNI:          true,
				WaitForCCM:          true,
				ObservedInstanceIDs: &observedInstanceIDs,
			}, &workloadKubeconfig)

			var initialWorkerNodeNames []string
			Eventually(func(g Gomega) {
				expectMachineDeploymentReadyReplicas(g, cfg.Namespace, machineDeploymentName, 1)
				workers := workerMachinesForDeployment(g, cfg.Namespace, clusterName, machineDeploymentName)
				g.Expect(workers).To(HaveLen(1))
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, workers)
				initialWorkerNodeNames = nodeNamesForMachines(workers)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			Expect(initialWorkerNodeNames).To(HaveLen(1))

			By("scaling workload workers up to three replicas")
			cmd = exec.Command("kubectl", "scale", "machinedeployment", machineDeploymentName, "-n", cfg.Namespace, "--replicas=3")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to scale workload MachineDeployment up")

			var scaledUpInstanceIDs []string
			var scaledUpWorkerNodeNames []string
			Eventually(func(g Gomega) {
				expectMachineDeploymentReadyReplicas(g, cfg.Namespace, machineDeploymentName, 3)
				machines := readyStackitMachines(g, cfg.Namespace, testID, 4)
				scaledUpInstanceIDs = instanceIDs(machines)
				workers := workerMachinesForDeployment(g, cfg.Namespace, clusterName, machineDeploymentName)
				g.Expect(workers).To(HaveLen(3))
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, workers)
				expectWorkloadNodesReady(g, workloadKubeconfig, 4)
				scaledUpWorkerNodeNames = nodeNamesForMachines(workers)
			}, 45*time.Minute, 15*time.Second).Should(Succeed())
			observedInstanceIDs = appendUnique(observedInstanceIDs, scaledUpInstanceIDs...)
			Expect(scaledUpWorkerNodeNames).To(HaveLen(3))
			for _, nodeName := range initialWorkerNodeNames {
				Expect(scaledUpWorkerNodeNames).To(ContainElement(nodeName))
			}

			By("verifying all scaled-up workload VMs exist in STACKIT")
			for _, instanceID := range scaledUpInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					server, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(server.ID).To(Equal(instanceID))
				}, 5*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("scaling workload workers back down to one replica")
			cmd = exec.Command("kubectl", "scale", "machinedeployment", machineDeploymentName, "-n", cfg.Namespace, "--replicas=1")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to scale workload MachineDeployment down")

			var remainingInstanceIDs []string
			var remainingWorkerNodeNames []string
			Eventually(func(g Gomega) {
				expectMachineDeploymentReadyReplicas(g, cfg.Namespace, machineDeploymentName, 1)
				machines := readyStackitMachines(g, cfg.Namespace, testID, 2)
				remainingInstanceIDs = instanceIDs(machines)
				workers := workerMachinesForDeployment(g, cfg.Namespace, clusterName, machineDeploymentName)
				g.Expect(workers).To(HaveLen(1))
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, workers)
				expectWorkloadNodesReady(g, workloadKubeconfig, 2)
				remainingWorkerNodeNames = nodeNamesForMachines(workers)
			}, 30*time.Minute, 15*time.Second).Should(Succeed())
			observedInstanceIDs = appendUnique(observedInstanceIDs, remainingInstanceIDs...)

			removedWorkerNodeNames := difference(scaledUpWorkerNodeNames, remainingWorkerNodeNames)
			Expect(removedWorkerNodeNames).To(HaveLen(2))

			By("verifying scaled-down workload Nodes were removed")
			Eventually(func(g Gomega) {
				expectWorkloadNodesGone(g, workloadKubeconfig, removedWorkerNodeNames)
			}, 10*time.Minute, 15*time.Second).Should(Succeed())

			deletedInstanceIDs := difference(scaledUpInstanceIDs, remainingInstanceIDs)
			Expect(deletedInstanceIDs).To(HaveLen(2))

			By("verifying scaled-down workload VMs were deleted from STACKIT")
			for _, instanceID := range deletedInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					_, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected server %s to be deleted, got %v", instanceID, err)
				}, 20*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("deleting the workload scale Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete workload scale Cluster")

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should replace worker VMs during a MachineDeployment version upgrade without STACKIT leaks", func() {
			if os.Getenv("STACKIT_E2E_UPGRADE_WORKERS") != "true" {
				Skip("set STACKIT_E2E_UPGRADE_WORKERS=true to run the real STACKIT MachineDeployment upgrade e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			clusterName := fmt.Sprintf("stackit-e2e-upgrade-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			machineDeploymentName := clusterName + "-md-0"
			upgradeFrom := envDefault("STACKIT_E2E_UPGRADE_FROM", defaultKubernetesVersion)
			upgradeTo := envDefault("STACKIT_E2E_UPGRADE_TO", "v1.34.8")
			validateSupportedKubernetesVersion(upgradeFrom)
			validateSupportedKubernetesVersion(upgradeTo)
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying an infra-only worker MachineDeployment upgrade fixture")
			fixture := renderStackitInfraOnlyMachineDeploymentFixture(clusterName, testID, cfg, upgradeFrom)
			fixturePath := writeTempManifest("stackit-md-upgrade-e2e-*.yaml", fixture)
			defer func() {
				cleanupStackitMachineDeploymentScaleFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT MachineDeployment upgrade fixture")

			By("waiting for the StackitCluster to become ready")
			Eventually(func(g Gomega) {
				output := kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", cfg.Namespace, "-o", "jsonpath={.status.ready}")
				g.Expect(output).To(Equal("true"))
			}, 15*time.Minute, 10*time.Second).Should(Succeed())

			By("waiting for the initial worker VM to provision")
			var initialInstanceIDs []string
			Eventually(func(g Gomega) {
				initialInstanceIDs = readyStackitMachineInstanceIDs(g, cfg.Namespace, testID, 1)
			}, 45*time.Minute, 15*time.Second).Should(Succeed())
			observedInstanceIDs = appendUnique(observedInstanceIDs, initialInstanceIDs...)
			Eventually(func(g Gomega) {
				expectCAPIMachinesWithProviderIDs(g, cfg.Namespace, clusterName, 1)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			By("patching the MachineDeployment Kubernetes version")
			patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"version":"%s"}}}}`, upgradeTo)
			cmd = exec.Command("kubectl", "patch", "machinedeployment", machineDeploymentName, "-n", cfg.Namespace, "--type=merge", "-p", patch)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to patch MachineDeployment version")

			By("waiting for the upgraded worker VM to provision")
			var upgradedMachines []stackitMachineItem
			Eventually(func(g Gomega) {
				upgradedMachines = readyStackitMachines(g, cfg.Namespace, testID, 2)
				g.Expect(instanceIDs(upgradedMachines)).To(ContainElement(initialInstanceIDs[0]))
			}, 45*time.Minute, 15*time.Second).Should(Succeed())
			upgradedInstanceIDs := instanceIDs(upgradedMachines)
			observedInstanceIDs = appendUnique(observedInstanceIDs, upgradedInstanceIDs...)
			Eventually(func(g Gomega) {
				expectCAPIMachinesWithProviderIDs(g, cfg.Namespace, clusterName, 2)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			replacementInstanceIDs := difference(upgradedInstanceIDs, initialInstanceIDs)
			Expect(replacementInstanceIDs).To(HaveLen(1))

			By("deleting the old worker Machine to exercise replacement VM cleanup")
			oldMachineName := stackitMachineNameForInstanceID(upgradedMachines, initialInstanceIDs[0])
			cmd = exec.Command("kubectl", "delete", "machine", oldMachineName, "-n", cfg.Namespace, "--wait=true", "--timeout=20m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete old worker Machine")

			Eventually(func(g Gomega) {
				remainingInstanceIDs := readyStackitMachineInstanceIDs(g, cfg.Namespace, testID, 1)
				g.Expect(remainingInstanceIDs).To(Equal(replacementInstanceIDs))
			}, 20*time.Minute, 15*time.Second).Should(Succeed())

			By("verifying the old worker VM was deleted from STACKIT")
			Eventually(func(g Gomega) {
				_, err := cloudClient.GetServer(ctx, initialInstanceIDs[0])
				g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected old server %s to be deleted, got %v", initialInstanceIDs[0], err)
			}, 20*time.Minute, 15*time.Second).Should(Succeed())

			By("deleting the upgraded workload Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete upgraded workload Cluster")

			By("ensuring all observed worker VMs are deleted via the STACKIT API")
			cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)

			By("verifying all observed worker VMs were deleted from STACKIT")
			for _, instanceID := range observedInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					_, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected server %s to be deleted, got %v", instanceID, err)
				}, 20*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should complete a workload worker MachineDeployment upgrade with Ready Nodes", func() {
			if os.Getenv("STACKIT_E2E_UPGRADE_WORKLOAD_WORKERS") != "true" {
				Skip("set STACKIT_E2E_UPGRADE_WORKLOAD_WORKERS=true to run the real workload MachineDeployment upgrade e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			credentials := stackitCredentialsSecret(ctx, cfg.CredentialsSecretName, cfg.CredentialsSecretNS)
			serviceAccountJSON, ok := credentials["serviceaccount.json"]
			Expect(ok).To(BeTrue(), "credentials Secret is missing serviceaccount.json")
			clusterName := fmt.Sprintf("stackit-e2e-wupgrade-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			machineDeploymentName := clusterName + "-md-0"
			upgradeFrom := envDefault("STACKIT_E2E_UPGRADE_FROM", defaultKubernetesVersion)
			upgradeTo := envDefault("STACKIT_E2E_UPGRADE_TO", "v1.35.4")
			validateSupportedKubernetesVersion(upgradeFrom)
			validateSupportedKubernetesVersion(upgradeTo)
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying a real kubeadm workload Cluster fixture for worker upgrade")
			fixture := renderKubeadmWorkloadClusterFixture(kubeadmWorkloadFixtureOptions{
				ClusterName:           clusterName,
				TestID:                testID,
				Config:                cfg,
				KubernetesVersion:     upgradeFrom,
				ServiceAccountJSON:    serviceAccountJSON,
				ControlPlaneReplicas:  1,
				WorkerReplicas:        2,
				APIServerLoadBalancer: true,
				IncludeCCMAddon:       true,
			})
			fixturePath := writeTempManifest("stackit-upgrade-workload-e2e-*.yaml", fixture)
			var workloadKubeconfig string
			defer func() {
				cleanupStackitKubeadmClusterFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				if workloadKubeconfig != "" {
					_ = os.Remove(workloadKubeconfig)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT workload worker upgrade fixture")

			waitForKubeadmWorkloadClusterReady(workloadReadinessOptions{
				Namespace:           cfg.Namespace,
				ClusterName:         clusterName,
				TestID:              testID,
				WantNodes:           3,
				WantMachines:        3,
				InstallCNI:          true,
				WaitForCCM:          true,
				ObservedInstanceIDs: &observedInstanceIDs,
			}, &workloadKubeconfig)

			var initialWorkerNodeNames []string
			var initialWorkerInstanceIDs []string
			Eventually(func(g Gomega) {
				expectMachineDeploymentReadyReplicas(g, cfg.Namespace, machineDeploymentName, 2)
				workers := workerMachinesForDeployment(g, cfg.Namespace, clusterName, machineDeploymentName)
				g.Expect(workers).To(HaveLen(2))
				expectMachinesVersion(g, workers, upgradeFrom)
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, workers)
				initialWorkerNodeNames = nodeNamesForMachines(workers)
				initialWorkerInstanceIDs = stackitInstanceIDsForMachines(g, cfg.Namespace, testID, workers)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			Expect(initialWorkerNodeNames).To(HaveLen(2))
			Expect(initialWorkerInstanceIDs).To(HaveLen(2))
			observedInstanceIDs = appendUnique(observedInstanceIDs, initialWorkerInstanceIDs...)

			By("patching the workload worker MachineDeployment Kubernetes version")
			patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"version":"%s"}}}}`, upgradeTo)
			cmd = exec.Command("kubectl", "patch", "machinedeployment", machineDeploymentName, "-n", cfg.Namespace, "--type=merge", "-p", patch)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to patch workload MachineDeployment version")

			var upgradedWorkerNodeNames []string
			var upgradedWorkerInstanceIDs []string
			Eventually(func(g Gomega) {
				expectMachineDeploymentRollout(g, cfg.Namespace, machineDeploymentName, 2)
				machines := readyStackitMachines(g, cfg.Namespace, testID, 3)
				observedInstanceIDs = appendUnique(observedInstanceIDs, instanceIDs(machines)...)
				workers := workerMachinesForDeployment(g, cfg.Namespace, clusterName, machineDeploymentName)
				g.Expect(workers).To(HaveLen(2))
				expectMachinesVersion(g, workers, upgradeTo)
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, workers)
				expectWorkloadNodesReady(g, workloadKubeconfig, 3)
				upgradedWorkerNodeNames = nodeNamesForMachines(workers)
				upgradedWorkerInstanceIDs = stackitInstanceIDsForMachines(g, cfg.Namespace, testID, workers)
			}, 60*time.Minute, 15*time.Second).Should(Succeed())
			Expect(upgradedWorkerNodeNames).To(HaveLen(2))
			Expect(upgradedWorkerInstanceIDs).To(HaveLen(2))

			oldWorkerNodeNames := difference(initialWorkerNodeNames, upgradedWorkerNodeNames)
			Expect(oldWorkerNodeNames).To(HaveLen(2))

			By("verifying old workload worker Nodes were removed")
			Eventually(func(g Gomega) {
				expectWorkloadNodesGone(g, workloadKubeconfig, oldWorkerNodeNames)
			}, 10*time.Minute, 15*time.Second).Should(Succeed())

			deletedInstanceIDs := difference(initialWorkerInstanceIDs, upgradedWorkerInstanceIDs)
			Expect(deletedInstanceIDs).To(HaveLen(2))

			By("verifying old workload worker VMs were deleted from STACKIT")
			for _, instanceID := range deletedInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					_, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected old server %s to be deleted, got %v", instanceID, err)
				}, 20*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("deleting the workload worker upgrade Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete workload worker upgrade Cluster")

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})

		It("should complete a workload control-plane upgrade with a Ready Node", func() {
			if os.Getenv("STACKIT_E2E_UPGRADE_WORKLOAD_CONTROL_PLANE") != "true" {
				Skip("set STACKIT_E2E_UPGRADE_WORKLOAD_CONTROL_PLANE=true to run the real workload KubeadmControlPlane upgrade e2e test")
			}

			cfg := stackitVMConfigFromEnv()
			ctx := context.Background()
			cloudClient := stackitCloudClientFromCredentialsSecret(ctx, cfg)
			credentials := stackitCredentialsSecret(ctx, cfg.CredentialsSecretName, cfg.CredentialsSecretNS)
			serviceAccountJSON, ok := credentials["serviceaccount.json"]
			Expect(ok).To(BeTrue(), "credentials Secret is missing serviceaccount.json")
			clusterName := fmt.Sprintf("stackit-e2e-cpupg-%d", time.Now().Unix())
			testID := envDefault("STACKIT_E2E_TEST_ID", clusterName)
			leakTags := stackitE2ETags(testID)
			kubeadmControlPlaneName := clusterName + "-control-plane"
			upgradeFrom := envDefault("STACKIT_E2E_UPGRADE_FROM", defaultKubernetesVersion)
			upgradeTo := envDefault("STACKIT_E2E_UPGRADE_TO", "v1.35.4")
			validateSupportedKubernetesVersion(upgradeFrom)
			validateSupportedKubernetesVersion(upgradeTo)
			observedInstanceIDs := []string{}

			By("cleaning up stale STACKIT resources for the e2e test ID")
			Expect(cloud.CleanupByTags(ctx, cloudClient, leakTags)).To(Succeed())

			By("applying a real kubeadm workload Cluster fixture for control-plane upgrade")
			fixture := renderKubeadmWorkloadClusterFixture(kubeadmWorkloadFixtureOptions{
				ClusterName:           clusterName,
				TestID:                testID,
				Config:                cfg,
				KubernetesVersion:     upgradeFrom,
				ServiceAccountJSON:    serviceAccountJSON,
				ControlPlaneReplicas:  1,
				WorkerReplicas:        1,
				APIServerLoadBalancer: true,
				IncludeCCMAddon:       true,
			})
			fixturePath := writeTempManifest("stackit-cp-upgrade-workload-e2e-*.yaml", fixture)
			var workloadKubeconfig string
			defer func() {
				cleanupStackitKubeadmClusterFixture(clusterName, cfg.Namespace)
				cleanupCloudServersByID(ctx, cloudClient, observedInstanceIDs)
				if err := cloud.CleanupByTags(ctx, cloudClient, leakTags); err != nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API cleanup warning: %v\n", err)
				}
				if workloadKubeconfig != "" {
					_ = os.Remove(workloadKubeconfig)
				}
				_ = os.Remove(fixturePath)
			}()

			cmd := exec.Command("kubectl", "apply", "-f", fixturePath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply STACKIT workload control-plane upgrade fixture")

			waitForKubeadmWorkloadClusterReady(workloadReadinessOptions{
				Namespace:           cfg.Namespace,
				ClusterName:         clusterName,
				TestID:              testID,
				WantNodes:           2,
				WantMachines:        2,
				InstallCNI:          true,
				WaitForCCM:          true,
				ObservedInstanceIDs: &observedInstanceIDs,
			}, &workloadKubeconfig)

			var initialControlPlaneInstanceIDs []string
			Eventually(func(g Gomega) {
				expectKubeadmControlPlaneReadyReplicas(g, cfg.Namespace, kubeadmControlPlaneName, 1)
				controlPlanes := controlPlaneMachinesForCluster(g, cfg.Namespace, clusterName)
				g.Expect(controlPlanes).To(HaveLen(1))
				expectMachinesVersion(g, controlPlanes, upgradeFrom)
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, controlPlanes)
				initialControlPlaneInstanceIDs = stackitInstanceIDsForMachines(g, cfg.Namespace, testID, controlPlanes)
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			Expect(initialControlPlaneInstanceIDs).To(HaveLen(1))
			observedInstanceIDs = appendUnique(observedInstanceIDs, initialControlPlaneInstanceIDs...)

			By("patching the workload KubeadmControlPlane Kubernetes version")
			patch := fmt.Sprintf(`{"spec":{"version":"%s"}}`, upgradeTo)
			cmd = exec.Command("kubectl", "patch", "kubeadmcontrolplane", kubeadmControlPlaneName, "-n", cfg.Namespace, "--type=merge", "-p", patch)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to patch workload KubeadmControlPlane version")

			var upgradedControlPlaneInstanceIDs []string
			Eventually(func(g Gomega) {
				output := kubectlOutputWithKubeconfig(g, workloadKubeconfig, "get", "ns", "kube-system", "-o", "name")
				g.Expect(output).To(Equal("namespace/kube-system"))
				expectKubeadmControlPlaneRollout(g, cfg.Namespace, kubeadmControlPlaneName, 1)
				machines := readyStackitMachines(g, cfg.Namespace, testID, 2)
				observedInstanceIDs = appendUnique(observedInstanceIDs, instanceIDs(machines)...)
				controlPlanes := controlPlaneMachinesForCluster(g, cfg.Namespace, clusterName)
				g.Expect(controlPlanes).To(HaveLen(1))
				expectMachinesVersion(g, controlPlanes, upgradeTo)
				expectWorkloadNodesReadyForMachines(g, workloadKubeconfig, controlPlanes)
				expectProviderIDNodeRefAlignment(g, cfg.Namespace, clusterName, testID, workloadKubeconfig, 2)
				upgradedControlPlaneInstanceIDs = stackitInstanceIDsForMachines(g, cfg.Namespace, testID, controlPlanes)
			}, 60*time.Minute, 15*time.Second).Should(Succeed())
			Expect(upgradedControlPlaneInstanceIDs).To(HaveLen(1))

			deletedInstanceIDs := difference(initialControlPlaneInstanceIDs, upgradedControlPlaneInstanceIDs)
			By("verifying old workload control-plane VMs were deleted from STACKIT when replaced")
			for _, instanceID := range deletedInstanceIDs {
				instanceID := instanceID
				Eventually(func(g Gomega) {
					_, err := cloudClient.GetServer(ctx, instanceID)
					g.Expect(cloud.IsNotFound(err)).To(BeTrue(), "expected old server %s to be deleted, got %v", instanceID, err)
				}, 20*time.Minute, 15*time.Second).Should(Succeed())
			}

			By("deleting the workload control-plane upgrade Cluster")
			cmd = exec.Command("kubectl", "delete", "cluster", clusterName, "-n", cfg.Namespace, "--wait=true", "--timeout=45m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete workload control-plane upgrade Cluster")

			By("verifying no tagged STACKIT resources remain for the e2e test ID")
			Eventually(func(g Gomega) {
				servers, err := cloudClient.ListServersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(servers).To(BeEmpty())
				loadBalancers, err := cloudClient.ListAPIServerLoadBalancersByTags(ctx, leakTags)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(loadBalancers).To(BeEmpty())
			}, 20*time.Minute, 15*time.Second).Should(Succeed())
		})
	})
})

type stackitVMConfig struct {
	Namespace             string
	ProjectID             string
	Region                string
	NetworkID             string
	ImageID               string
	MachineType           string
	AvailabilityZone      string
	SSHKeyName            string
	SecurityGroupIDs      []string
	BastionEnabled        bool
	BastionImageID        string
	BastionMachineType    string
	BastionSSHKeyName     string
	BastionAllowedCIDRs   []string
	CredentialsSecretName string
	CredentialsSecretNS   string
	RootVolumeSizeGiB     string
	RootVolumePerformance string
}

type kubeadmWorkloadFixtureOptions struct {
	ClusterName           string
	TestID                string
	Config                stackitVMConfig
	KubernetesVersion     string
	ServiceAccountJSON    []byte
	ControlPlaneReplicas  int
	WorkerReplicas        int
	APIServerLoadBalancer bool
	IncludeCCMAddon       bool
}

type topologyWorkloadFixtureOptions struct {
	ClusterName        string
	TestID             string
	Config             stackitVMConfig
	KubernetesVersion  string
	ServiceAccountJSON []byte
}

type workloadReadinessOptions struct {
	Namespace           string
	ClusterName         string
	TestID              string
	WantNodes           int
	WantMachines        int
	InstallCNI          bool
	WaitForCCM          bool
	Topology            bool
	ObservedInstanceIDs *[]string
}

func stackitVMConfigFromEnv() stackitVMConfig {
	imageID := requiredEnv("STACKIT_IMAGE_ID")
	sshKeyName := os.Getenv("STACKIT_SSH_KEY_NAME")
	return stackitVMConfig{
		Namespace:             envDefault("STACKIT_E2E_NAMESPACE", "default"),
		ProjectID:             requiredEnv("STACKIT_PROJECT_ID"),
		Region:                envDefault("STACKIT_REGION", "eu01"),
		NetworkID:             requiredEnv("STACKIT_NETWORK_ID"),
		ImageID:               imageID,
		MachineType:           stackitE2EMachineType,
		AvailabilityZone:      requiredEnv("STACKIT_AVAILABILITY_ZONE"),
		SSHKeyName:            sshKeyName,
		SecurityGroupIDs:      splitCSV(os.Getenv("STACKIT_SECURITY_GROUP_IDS")),
		BastionEnabled:        os.Getenv("STACKIT_E2E_BASTION") == "true",
		BastionImageID:        envDefault("STACKIT_BASTION_IMAGE_ID", imageID),
		BastionMachineType:    envDefault("STACKIT_BASTION_MACHINE_TYPE", "c2i.1"),
		BastionSSHKeyName:     envDefault("STACKIT_BASTION_SSH_KEY_NAME", sshKeyName),
		BastionAllowedCIDRs:   splitCSV(envDefault("STACKIT_BASTION_ALLOWED_CIDRS", "0.0.0.0/0")),
		CredentialsSecretName: envDefault("STACKIT_CREDENTIALS_SECRET_NAME", "stackit-credentials"),
		CredentialsSecretNS:   envDefault("STACKIT_CREDENTIALS_SECRET_NAMESPACE", envDefault("STACKIT_E2E_NAMESPACE", "default")),
		RootVolumeSizeGiB:     envDefault("STACKIT_ROOT_VOLUME_SIZE_GIB", "50"),
		RootVolumePerformance: envDefault("STACKIT_ROOT_VOLUME_PERFORMANCE_CLASS", "storage_premium_perf6"),
	}
}

func stackitCloudClientFromCredentialsSecret(ctx context.Context, cfg stackitVMConfig) cloud.Client {
	secret := stackitCredentialsSecret(ctx, cfg.CredentialsSecretName, cfg.CredentialsSecretNS)
	serviceAccountJSON, ok := secret["serviceaccount.json"]
	Expect(ok).To(BeTrue(), "credentials Secret is missing serviceaccount.json")
	Expect(serviceAccountJSON).NotTo(BeEmpty(), "credentials Secret serviceaccount.json is empty")

	client, err := cloud.NewClient(ctx, cloud.Credentials{
		ProjectID:          cfg.ProjectID,
		Region:             cfg.Region,
		ServiceAccountJSON: serviceAccountJSON,
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create STACKIT cloud client")
	return client
}

func stackitCredentialsSecret(_ context.Context, name, namespace string) map[string][]byte {
	cmd := exec.Command("kubectl", "get", "secret", name, "-n", namespace, "-o", "json")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to read STACKIT credentials Secret")

	var secret struct {
		Data map[string]string `json:"data"`
	}
	Expect(json.Unmarshal([]byte(output), &secret)).To(Succeed())
	out := map[string][]byte{}
	for key, value := range secret.Data {
		decoded, err := base64.StdEncoding.DecodeString(value)
		Expect(err).NotTo(HaveOccurred(), "Failed to decode Secret key %s", key)
		out[key] = decoded
	}
	return out
}

func renderStackitVMFixture(clusterName, machineName, testID string, cfg stackitVMConfig) string {
	securityGroups := ""
	for _, securityGroupID := range cfg.SecurityGroupIDs {
		securityGroups += fmt.Sprintf("\n        - %s", securityGroupID)
	}
	if securityGroups != "" {
		securityGroups = "\n      securityGroups:" + securityGroups
	}

	sshKeyName := ""
	if cfg.SSHKeyName != "" {
		sshKeyName = fmt.Sprintf("\n  sshKeyName: %s", cfg.SSHKeyName)
	}

	return fmt.Sprintf(`apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: %[1]s
  namespace: %[3]s
spec:
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitCluster
    name: %[1]s
  clusterNetwork:
    pods:
      cidrBlocks:
        - 192.168.0.0/16
    services:
      cidrBlocks:
        - 10.128.0.0/12
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitCluster
metadata:
  name: %[1]s
  namespace: %[3]s
  labels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[16]s"
spec:
  projectID: %[4]s
  region: %[5]s
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[16]s"
  credentialsSecretRef:
    name: %[6]s
    namespace: %[7]s
  network:
    id: %[8]s
  apiServerLoadBalancer:
    enabled: false
  controlPlaneEndpoint:
    host: 203.0.113.10
    port: 6443
---
apiVersion: v1
kind: Secret
metadata:
  name: %[2]s-bootstrap
  namespace: %[3]s
type: Opaque
data:
  value: IyEvYmluL3NoCmVjaG8gc3RhY2tpdC1lMmUK
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: Machine
metadata:
  name: %[2]s
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
spec:
  clusterName: %[1]s
  bootstrap:
    dataSecretName: %[2]s-bootstrap
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitMachine
    name: %[2]s
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachine
metadata:
  name: %[2]s
  namespace: %[3]s
  labels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[16]s"
spec:
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[16]s"
  imageID: %[9]s
  machineType: %[10]s
  availabilityZone: %[11]s%[12]s
  rootVolume:
    sizeGiB: %[13]s
    performanceClass: %[14]s
    deleteOnTermination: true
  network:
    id: %[8]s%[15]s
`, clusterName, machineName, cfg.Namespace, cfg.ProjectID, cfg.Region, cfg.CredentialsSecretName, cfg.CredentialsSecretNS,
		cfg.NetworkID, cfg.ImageID, cfg.MachineType, cfg.AvailabilityZone, sshKeyName, cfg.RootVolumeSizeGiB,
		cfg.RootVolumePerformance, securityGroups, testID)
}

func renderStackitClusterFixture(clusterName, testID string, cfg stackitVMConfig) string {
	securityGroups := ""
	for _, securityGroupID := range cfg.SecurityGroupIDs {
		securityGroups += fmt.Sprintf("\n        - %s", securityGroupID)
	}
	if securityGroups != "" {
		securityGroups = "\n      securityGroups:" + securityGroups
	}

	sshKeyName := ""
	if cfg.SSHKeyName != "" {
		sshKeyName = fmt.Sprintf("\n  sshKeyName: %s", cfg.SSHKeyName)
	}
	bastion := ""
	if cfg.BastionEnabled {
		Expect(cfg.BastionSSHKeyName).NotTo(BeEmpty(), "STACKIT_BASTION_SSH_KEY_NAME or STACKIT_SSH_KEY_NAME is required when STACKIT_E2E_BASTION=true")
		allowedCIDRs := ""
		for _, cidr := range cfg.BastionAllowedCIDRs {
			allowedCIDRs += fmt.Sprintf("\n      - %s", cidr)
		}
		bastion = fmt.Sprintf(`
  bastion:
    enabled: true
    imageID: %s
    machineType: %s
    sshKeyName: %s
    allowedCIDRs:%s
    rootVolume:
      sizeGiB: %s
      performanceClass: %s
      deleteOnTermination: true`, cfg.BastionImageID, cfg.BastionMachineType, cfg.BastionSSHKeyName, allowedCIDRs, cfg.RootVolumeSizeGiB, cfg.RootVolumePerformance)
	}
	controlPlaneMachineName := clusterName + "-control-plane-0"
	workerMachineName := clusterName + "-worker-0"

	return fmt.Sprintf(`apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: %[1]s
  namespace: %[5]s
spec:
  clusterNetwork:
    pods:
      cidrBlocks:
        - 192.168.0.0/16
    services:
      cidrBlocks:
        - 10.128.0.0/12
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitCluster
    name: %[1]s
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitCluster
metadata:
  name: %[1]s
  namespace: %[5]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
spec:
  projectID: %[6]s
  region: %[7]s
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
  credentialsSecretRef:
    name: %[8]s
    namespace: %[9]s
  network:
    id: %[10]s
  apiServerLoadBalancer:
    enabled: true%[18]s
---
apiVersion: v1
kind: Secret
metadata:
  name: %[2]s-bootstrap
  namespace: %[5]s
type: Opaque
data:
  value: IyEvYmluL3NoCmVjaG8gc3RhY2tpdC1lMmUtY29udHJvbC1wbGFuZQo=
---
apiVersion: v1
kind: Secret
metadata:
  name: %[3]s-bootstrap
  namespace: %[5]s
type: Opaque
data:
  value: IyEvYmluL3NoCmVjaG8gc3RhY2tpdC1lMmUtd29ya2VyCg==
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: Machine
metadata:
  name: %[2]s
  namespace: %[5]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster.x-k8s.io/control-plane: ""
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
spec:
  clusterName: %[1]s
  bootstrap:
    dataSecretName: %[2]s-bootstrap
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitMachine
    name: %[2]s
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachine
metadata:
  name: %[2]s
  namespace: %[5]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
spec:
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
  imageID: %[11]s
  machineType: %[12]s
  availabilityZone: %[13]s%[14]s
  rootVolume:
    sizeGiB: %[15]s
    performanceClass: %[16]s
    deleteOnTermination: true
  network:
    id: %[10]s%[17]s
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: Machine
metadata:
  name: %[3]s
  namespace: %[5]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
spec:
  clusterName: %[1]s
  bootstrap:
    dataSecretName: %[3]s-bootstrap
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitMachine
    name: %[3]s
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachine
metadata:
  name: %[3]s
  namespace: %[5]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
spec:
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[4]s"
  imageID: %[11]s
  machineType: %[12]s
  availabilityZone: %[13]s%[14]s
  rootVolume:
    sizeGiB: %[15]s
    performanceClass: %[16]s
    deleteOnTermination: true
  network:
    id: %[10]s%[17]s
`, clusterName, controlPlaneMachineName, workerMachineName, testID, cfg.Namespace, cfg.ProjectID, cfg.Region,
		cfg.CredentialsSecretName, cfg.CredentialsSecretNS, cfg.NetworkID, cfg.ImageID, cfg.MachineType,
		cfg.AvailabilityZone, sshKeyName, cfg.RootVolumeSizeGiB, cfg.RootVolumePerformance, securityGroups, bastion)
}

func renderStackitInfraOnlyMachineDeploymentFixture(clusterName, testID string, cfg stackitVMConfig, kubernetesVersion string) string {
	securityGroups := ""
	for _, securityGroupID := range cfg.SecurityGroupIDs {
		securityGroups += fmt.Sprintf("\n        - %s", securityGroupID)
	}
	if securityGroups != "" {
		securityGroups = "\n      securityGroups:" + securityGroups
	}

	sshKeyName := ""
	if cfg.SSHKeyName != "" {
		sshKeyName = fmt.Sprintf("\n  sshKeyName: %s", cfg.SSHKeyName)
	}

	return fmt.Sprintf(`apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: %[1]s
  namespace: %[3]s
spec:
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitCluster
    name: %[1]s
  clusterNetwork:
    pods:
      cidrBlocks:
        - 192.168.0.0/16
    services:
      cidrBlocks:
        - 10.128.0.0/12
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitCluster
metadata:
  name: %[1]s
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  projectID: %[4]s
  region: %[5]s
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
  credentialsSecretRef:
    name: %[6]s
    namespace: %[7]s
  network:
    id: %[8]s
  apiServerLoadBalancer:
    enabled: false
  controlPlaneEndpoint:
    host: 203.0.113.10
    port: 6443
---
apiVersion: v1
kind: Secret
metadata:
  name: %[1]s-worker-bootstrap
  namespace: %[3]s
type: Opaque
data:
  value: IyEvYmluL3NoCmVjaG8gc3RhY2tpdC1lMmUtc2NhbGUtd29ya2VyCg==
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineDeployment
metadata:
  name: %[1]s-md-0
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  clusterName: %[1]s
  replicas: 1
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: %[1]s
      cluster.x-k8s.io/deployment-name: %[1]s-md-0
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: %[1]s
        cluster.x-k8s.io/deployment-name: %[1]s-md-0
        cluster-api-provider-stackit/e2e: "true"
        cluster-api-provider-stackit/test-id: "%[2]s"
    spec:
      clusterName: %[1]s
      version: %[16]s
      bootstrap:
        dataSecretName: %[1]s-worker-bootstrap
      infrastructureRef:
        apiGroup: infrastructure.cluster.x-k8s.io
        kind: StackitMachineTemplate
        name: %[1]s-md-0
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachineTemplate
metadata:
  name: %[1]s-md-0
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  template:
    spec:
      additionalLabels:
        cluster-api-provider-stackit/e2e: "true"
        cluster-api-provider-stackit/test-id: "%[2]s"
      imageID: %[9]s
      machineType: %[10]s
      availabilityZone: %[11]s%[12]s
      rootVolume:
        sizeGiB: %[13]s
        performanceClass: %[14]s
        deleteOnTermination: true
      network:
        id: %[8]s%[15]s
`, clusterName, testID, cfg.Namespace, cfg.ProjectID, cfg.Region, cfg.CredentialsSecretName, cfg.CredentialsSecretNS,
		cfg.NetworkID, cfg.ImageID, cfg.MachineType, cfg.AvailabilityZone, sshKeyName, cfg.RootVolumeSizeGiB,
		cfg.RootVolumePerformance, securityGroups, kubernetesVersion)
}

func renderStackitKubeadmClusterFixture(clusterName, testID string, cfg stackitVMConfig, kubernetesVersion string, serviceAccountJSON []byte) string {
	return renderKubeadmWorkloadClusterFixture(kubeadmWorkloadFixtureOptions{
		ClusterName:           clusterName,
		TestID:                testID,
		Config:                cfg,
		KubernetesVersion:     kubernetesVersion,
		ServiceAccountJSON:    serviceAccountJSON,
		ControlPlaneReplicas:  1,
		WorkerReplicas:        1,
		APIServerLoadBalancer: true,
		IncludeCCMAddon:       true,
	})
}

func renderTopologyClusterClassFixture() string {
	clusterClass, err := os.ReadFile("templates/clusterclass.yaml")
	Expect(err).NotTo(HaveOccurred(), "Failed to read topology ClusterClass template")
	return string(clusterClass)
}

func renderTopologyWorkloadClusterFixture(opts topologyWorkloadFixtureOptions) string {
	cfg := opts.Config
	rendered := renderTemplateFile("templates/cluster-template-topology.yaml",
		"${CLUSTER_CLASS_NAMESPACE:-${NAMESPACE}}", cfg.Namespace,
		"${STACKIT_E2E_TEST_ID:-${CLUSTER_NAME}}", opts.TestID,
		"${STACKIT_E2E_LABEL:-false}", "true",
		"${CLUSTER_NAME}", opts.ClusterName,
		"${NAMESPACE}", cfg.Namespace,
		"${KUBERNETES_VERSION}", opts.KubernetesVersion,
		"${CONTROL_PLANE_MACHINE_COUNT}", "1",
		"${WORKER_MACHINE_COUNT}", "1",
		"${STACKIT_PROJECT_ID}", cfg.ProjectID,
		"${STACKIT_REGION}", cfg.Region,
		"${STACKIT_NETWORK_ID}", cfg.NetworkID,
		"${STACKIT_IMAGE_ID}", cfg.ImageID,
		"${STACKIT_MACHINE_TYPE}", cfg.MachineType,
		"${STACKIT_CREDENTIALS_SECRET_NAME}", cfg.CredentialsSecretName,
		"${KUBERNETES_APT_REPOSITORY_MINOR}", kubernetesAptRepoMinor(opts.KubernetesVersion),
		"${STACKIT_SERVICE_ACCOUNT_JSON_B64}", base64.StdEncoding.EncodeToString(opts.ServiceAccountJSON),
		"${STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE}", cloudProviderStackitImageForKubernetesVersion(opts.KubernetesVersion),
	)
	Expect(rendered).NotTo(ContainSubstring("${"), "topology workload fixture contains unresolved template variables")
	return rendered
}

func renderKubeadmWorkloadClusterFixture(opts kubeadmWorkloadFixtureOptions) string {
	clusterName := opts.ClusterName
	testID := opts.TestID
	cfg := opts.Config
	kubernetesVersion := opts.KubernetesVersion
	serviceAccountJSON := opts.ServiceAccountJSON
	controlPlaneReplicas := opts.ControlPlaneReplicas
	if controlPlaneReplicas == 0 {
		controlPlaneReplicas = 1
	}
	workerReplicas := opts.WorkerReplicas
	if workerReplicas == 0 {
		workerReplicas = 1
	}

	securityGroups := ""
	for _, securityGroupID := range cfg.SecurityGroupIDs {
		securityGroups += fmt.Sprintf("\n        - %s", securityGroupID)
	}
	if securityGroups != "" {
		securityGroups = "\n      securityGroups:" + securityGroups
	}

	sshKeyName := ""
	if cfg.SSHKeyName != "" {
		sshKeyName = fmt.Sprintf("\n  sshKeyName: %s", cfg.SSHKeyName)
	}
	kubernetesRepoMinor := kubernetesAptRepoMinor(kubernetesVersion)
	controlPlanePreKubeadmCommands := indentBlock(kubeadmPackageInstallCommands(kubernetesRepoMinor), 6)
	workerPreKubeadmCommands := indentBlock(kubeadmPackageInstallCommands(kubernetesRepoMinor), 8)
	cloudProviderStackitAddon := indentBlock(renderCloudProviderStackitAddon(clusterName, cfg, kubernetesVersion, serviceAccountJSON), 4)

	return fmt.Sprintf(`apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: %[1]s
  namespace: %[3]s
  labels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
    cluster-api-provider-stackit/cloud-provider-stackit: "true"
spec:
  clusterNetwork:
    pods:
      cidrBlocks:
        - 192.168.0.0/16
    services:
      cidrBlocks:
        - 10.128.0.0/12
  infrastructureRef:
    apiGroup: infrastructure.cluster.x-k8s.io
    kind: StackitCluster
    name: %[1]s
  controlPlaneRef:
    apiGroup: controlplane.cluster.x-k8s.io
    kind: KubeadmControlPlane
    name: %[1]s-control-plane
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitCluster
metadata:
  name: %[1]s
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  projectID: %[4]s
  region: %[5]s
  additionalLabels:
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
  credentialsSecretRef:
    name: %[6]s
    namespace: %[7]s
  network:
    id: %[8]s
  apiServerLoadBalancer:
    enabled: true
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta2
kind: KubeadmControlPlane
metadata:
  name: %[1]s-control-plane
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  replicas: %[20]d
  version: %[16]s
  machineTemplate:
    metadata:
      labels:
        cluster-api-provider-stackit/e2e: "true"
        cluster-api-provider-stackit/test-id: "%[2]s"
    spec:
      infrastructureRef:
        apiGroup: infrastructure.cluster.x-k8s.io
        kind: StackitMachineTemplate
        name: %[1]s-control-plane
  kubeadmConfigSpec:
    preKubeadmCommands:
%[17]s
    initConfiguration:
      nodeRegistration:
        name: '{{ ds.meta_data.local_hostname }}'
        criSocket: unix:///var/run/containerd/containerd.sock
        ignorePreflightErrors:
          - NumCPU
        kubeletExtraArgs:
          - name: cloud-provider
            value: external
    joinConfiguration:
      timeouts:
        tlsBootstrapSeconds: 300
      nodeRegistration:
        name: '{{ ds.meta_data.local_hostname }}'
        criSocket: unix:///var/run/containerd/containerd.sock
        ignorePreflightErrors:
          - NumCPU
        kubeletExtraArgs:
          - name: cloud-provider
            value: external
    clusterConfiguration:
      controllerManager:
        extraArgs:
          - name: cloud-provider
            value: external
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachineTemplate
metadata:
  name: %[1]s-control-plane
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  template:
    spec:
      additionalLabels:
        cluster-api-provider-stackit/e2e: "true"
        cluster-api-provider-stackit/test-id: "%[2]s"
      imageID: %[9]s
      machineType: %[10]s
      availabilityZone: %[11]s%[12]s
      rootVolume:
        sizeGiB: %[13]s
        performanceClass: %[14]s
        deleteOnTermination: true
      network:
        id: %[8]s%[15]s
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineDeployment
metadata:
  name: %[1]s-md-0
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  clusterName: %[1]s
  replicas: %[21]d
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: %[1]s
      cluster.x-k8s.io/deployment-name: %[1]s-md-0
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: %[1]s
        cluster.x-k8s.io/deployment-name: %[1]s-md-0
        cluster-api-provider-stackit/e2e: "true"
        cluster-api-provider-stackit/test-id: "%[2]s"
    spec:
      clusterName: %[1]s
      version: %[16]s
      bootstrap:
        configRef:
          apiGroup: bootstrap.cluster.x-k8s.io
          kind: KubeadmConfigTemplate
          name: %[1]s-md-0
      infrastructureRef:
        apiGroup: infrastructure.cluster.x-k8s.io
        kind: StackitMachineTemplate
        name: %[1]s-md-0
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: StackitMachineTemplate
metadata:
  name: %[1]s-md-0
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  template:
    spec:
      additionalLabels:
        cluster-api-provider-stackit/e2e: "true"
        cluster-api-provider-stackit/test-id: "%[2]s"
      imageID: %[9]s
      machineType: %[10]s
      availabilityZone: %[11]s%[12]s
      rootVolume:
        sizeGiB: %[13]s
        performanceClass: %[14]s
        deleteOnTermination: true
      network:
        id: %[8]s%[15]s
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta2
kind: KubeadmConfigTemplate
metadata:
  name: %[1]s-md-0
  namespace: %[3]s
  labels:
    cluster.x-k8s.io/cluster-name: %[1]s
    cluster-api-provider-stackit/e2e: "true"
    cluster-api-provider-stackit/test-id: "%[2]s"
spec:
  template:
    spec:
      preKubeadmCommands:
%[18]s
      joinConfiguration:
        timeouts:
          tlsBootstrapSeconds: 300
        nodeRegistration:
          name: '{{ ds.meta_data.local_hostname }}'
          criSocket: unix:///var/run/containerd/containerd.sock
          ignorePreflightErrors:
            - NumCPU
          kubeletExtraArgs:
            - name: cloud-provider
              value: external
---
apiVersion: addons.cluster.x-k8s.io/v1beta2
kind: ClusterResourceSet
metadata:
  name: %[1]s-cloud-provider-stackit
  namespace: %[3]s
spec:
  strategy: Reconcile
  clusterSelector:
    matchLabels:
      cluster-api-provider-stackit/cloud-provider-stackit: "true"
  resources:
    - kind: Secret
      name: %[1]s-cloud-provider-stackit
---
apiVersion: v1
kind: Secret
metadata:
  name: %[1]s-cloud-provider-stackit
  namespace: %[3]s
type: addons.cluster.x-k8s.io/resource-set
stringData:
  cloud-provider-stackit.yaml: |
%[19]s
`, clusterName, testID, cfg.Namespace, cfg.ProjectID, cfg.Region, cfg.CredentialsSecretName, cfg.CredentialsSecretNS,
		cfg.NetworkID, cfg.ImageID, cfg.MachineType, cfg.AvailabilityZone, sshKeyName, cfg.RootVolumeSizeGiB,
		cfg.RootVolumePerformance, securityGroups, kubernetesVersion, controlPlanePreKubeadmCommands, workerPreKubeadmCommands,
		cloudProviderStackitAddon, controlPlaneReplicas, workerReplicas)
}

func kubernetesAptRepoMinor(kubernetesVersion string) string {
	version := strings.TrimPrefix(kubernetesVersion, "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return strings.TrimSuffix(defaultKubernetesVersion, ".0")
	}
	return "v" + parts[0] + "." + parts[1]
}

func validateSupportedKubernetesVersion(kubernetesVersion string) {
	minor, ok := kubernetesMinor(kubernetesVersion)
	Expect(ok).To(BeTrue(), "Kubernetes version %q must use v<major>.<minor>.<patch> format", kubernetesVersion)
	Expect(supportedKubernetesMinor(minor)).To(BeTrue(), "Kubernetes minor %s is unsupported; supported minors are v1.33.x, v1.34.x, v1.35.x, and v1.36.x", minor)
}

func cloudProviderStackitImageForKubernetesVersion(kubernetesVersion string) string {
	minor, ok := kubernetesMinor(kubernetesVersion)
	Expect(ok).To(BeTrue(), "Kubernetes version %q must use v<major>.<minor>.<patch> format", kubernetesVersion)
	defaultImage, ok := defaultCloudProviderStackitImages[minor]
	Expect(ok).To(BeTrue(), "no default cloud-provider-stackit image configured for Kubernetes minor %s", minor)
	image := envDefault("STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE", defaultImage)
	imageMinor, ok := imageKubernetesMinor(image)
	Expect(ok).To(BeTrue(), "cloud-provider-stackit image %q must include a v<major>.<minor>.<patch> tag", image)
	Expect(imageMinor).To(Equal(minor), "cloud-provider-stackit image minor must match Kubernetes minor")
	return image
}

func kubernetesMinor(version string) (string, bool) {
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "." + parts[1], true
}

func imageKubernetesMinor(image string) (string, bool) {
	tagStart := strings.LastIndex(image, ":")
	if tagStart == -1 || tagStart == len(image)-1 {
		return "", false
	}
	tag := image[tagStart+1:]
	if digestStart := strings.Index(tag, "@"); digestStart != -1 {
		tag = tag[:digestStart]
	}
	return kubernetesMinor(tag)
}

func supportedKubernetesMinor(minor string) bool {
	switch minor {
	case "1.33", "1.34", "1.35", "1.36":
		return true
	default:
		return false
	}
}

func kubeadmPackageInstallCommands(kubernetesRepoMinor string) string {
	return fmt.Sprintf(`- |
  set -eu
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y apt-transport-https ca-certificates conntrack curl gpg containerd
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://pkgs.k8s.io/core:/stable:/%[1]s/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
  chmod 0644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg
  echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/%[1]s/deb/ /' > /etc/apt/sources.list.d/kubernetes.list
  apt-get update
  apt-get install -y kubelet kubeadm kubectl
  apt-mark hold kubelet kubeadm kubectl
  mkdir -p /etc/containerd
  containerd config default > /etc/containerd/config.toml
  sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
  systemctl restart containerd
  modprobe br_netfilter
  printf 'net.bridge.bridge-nf-call-iptables=1\nnet.ipv4.ip_forward=1\n' > /etc/sysctl.d/99-kubernetes-cri.conf
  sysctl --system
  (journalctl -fu kubelet --no-pager > /dev/console 2>&1 &)`, kubernetesRepoMinor)
}

func indentBlock(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(value, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func stackitE2ETags(testID string) map[string]string {
	return map[string]string{
		util.LabelE2E:    util.E2EValue,
		util.LabelTestID: testID,
	}
}

func stackitE2EBastionTags(testID string) map[string]string {
	tags := stackitE2ETags(testID)
	tags[util.LabelResourceRole] = util.ResourceRoleBastion
	return tags
}

func stackitE2ENodeSSHTags(testID string) map[string]string {
	tags := stackitE2ETags(testID)
	tags[util.LabelResourceRole] = util.ResourceRoleNodeSSH
	return tags
}

func cleanupStackitClusterFixture(clusterName, namespace string) {
	for _, args := range [][]string{
		{"delete", "cluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=45m"},
		{"delete", "machine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitmachine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitcluster", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=10m"},
		{"delete", "secret", clusterName + "-control-plane-0-bootstrap", "-n", namespace, "--ignore-not-found"},
		{"delete", "secret", clusterName + "-worker-0-bootstrap", "-n", namespace, "--ignore-not-found"},
	} {
		cmd := exec.Command("kubectl", args...)
		if _, err := utils.Run(cmd); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "cleanup warning: %v\n", err)
		}
	}
}

func cleanupStackitMachineDeploymentScaleFixture(clusterName, namespace string) {
	for _, args := range [][]string{
		{"delete", "cluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=45m"},
		{"delete", "machinedeployment", clusterName + "-md-0", "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "machine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitmachine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitcluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=10m"},
		{"delete", "stackitmachinetemplate", clusterName + "-md-0", "-n", namespace, "--ignore-not-found"},
		{"delete", "secret", clusterName + "-worker-bootstrap", "-n", namespace, "--ignore-not-found"},
	} {
		cmd := exec.Command("kubectl", args...)
		if _, err := utils.Run(cmd); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "cleanup warning: %v\n", err)
		}
	}
}

func cleanupStackitKubeadmClusterFixture(clusterName, namespace string) {
	for _, args := range [][]string{
		{"delete", "cluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=45m"},
		{"delete", "kubeadmcontrolplane", clusterName + "-control-plane", "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "machinedeployment", clusterName + "-md-0", "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "machine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitmachine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitcluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=10m"},
		{"delete", "clusterresourceset", clusterName + "-cloud-provider-stackit", "-n", namespace, "--ignore-not-found"},
		{"delete", "stackitmachinetemplate", clusterName + "-control-plane", "-n", namespace, "--ignore-not-found"},
		{"delete", "stackitmachinetemplate", clusterName + "-md-0", "-n", namespace, "--ignore-not-found"},
		{"delete", "kubeadmconfigtemplate", clusterName + "-md-0", "-n", namespace, "--ignore-not-found"},
		{"delete", "secret", clusterName + "-cloud-provider-stackit", "-n", namespace, "--ignore-not-found"},
		{"delete", "secret", clusterName + "-kubeconfig", "-n", namespace, "--ignore-not-found"},
	} {
		cmd := exec.Command("kubectl", args...)
		if _, err := utils.Run(cmd); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "cleanup warning: %v\n", err)
		}
	}
}

func cleanupStackitTopologyClusterFixture(clusterName, namespace string) {
	for _, args := range [][]string{
		{"delete", "cluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=45m"},
		{"delete", "machine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitmachine", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name=" + clusterName, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "stackitcluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=10m"},
		{"delete", "clusterresourceset", clusterName + "-cloud-provider-stackit", "-n", namespace, "--ignore-not-found"},
		{"delete", "secret", clusterName + "-cloud-provider-stackit", "-n", namespace, "--ignore-not-found"},
		{"delete", "secret", clusterName + "-kubeconfig", "-n", namespace, "--ignore-not-found"},
		{"delete", "clusterclass", "stackit", "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=10m"},
		{"delete", "stackitclustertemplate", "stackit-cluster", "-n", namespace, "--ignore-not-found"},
		{"delete", "kubeadmcontrolplanetemplate", "stackit-control-plane", "-n", namespace, "--ignore-not-found"},
		{"delete", "stackitmachinetemplate", "stackit-control-plane", "-n", namespace, "--ignore-not-found"},
		{"delete", "stackitmachinetemplate", "stackit-default-worker", "-n", namespace, "--ignore-not-found"},
		{"delete", "kubeadmconfigtemplate", "stackit-default-worker", "-n", namespace, "--ignore-not-found"},
	} {
		cmd := exec.Command("kubectl", args...)
		if _, err := utils.Run(cmd); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "cleanup warning: %v\n", err)
		}
	}
}

type stackitMachineList struct {
	Items []stackitMachineItem `json:"items"`
}

type stackitClusterList struct {
	Items []stackitClusterItem `json:"items"`
}

type stackitClusterItem struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		Ready bool `json:"ready"`
	} `json:"status"`
}

type stackitMachineItem struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		AdditionalLabels map[string]string `json:"additionalLabels"`
		ProviderID       *string           `json:"providerID"`
	} `json:"spec"`
	Status struct {
		Ready      bool   `json:"ready"`
		InstanceID string `json:"instanceID"`
		ProviderID string `json:"providerID"`
	} `json:"status"`
}

func expectNamedStackitClusterReady(g Gomega, namespace, clusterName string) {
	output := kubectlOutput(g, "get", "stackitcluster", clusterName, "-n", namespace, "-o", "jsonpath={.status.ready}")
	g.Expect(output).To(Equal("true"))
}

func expectTopologyStackitClusterReady(g Gomega, namespace, clusterName string) {
	cluster := topologyStackitCluster(g, namespace, clusterName)
	g.Expect(cluster.Status.Ready).To(BeTrue(), "StackitCluster %s is not ready", cluster.Metadata.Name)
}

func topologyStackitCluster(g Gomega, namespace, clusterName string) stackitClusterItem {
	cmd := exec.Command("kubectl", "get", "stackitclusters", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name="+clusterName, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var list stackitClusterList
	g.Expect(json.Unmarshal([]byte(output), &list)).To(Succeed())
	g.Expect(list.Items).To(HaveLen(1))
	return list.Items[0]
}

func stackitMachinesForTestID(g Gomega, namespace, testID string) []stackitMachineItem {
	cmd := exec.Command("kubectl", "get", "stackitmachines", "-n", namespace, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var list stackitMachineList
	g.Expect(json.Unmarshal([]byte(output), &list)).To(Succeed())
	out := []stackitMachineItem{}
	for _, machine := range list.Items {
		if machine.Spec.AdditionalLabels[util.LabelTestID] == testID {
			out = append(out, machine)
		}
	}
	return out
}

type capiMachineList struct {
	Items []capiMachineItem `json:"items"`
}

type capiMachineItem struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Version           string  `json:"version"`
		ProviderID        *string `json:"providerID"`
		InfrastructureRef struct {
			APIGroup string `json:"apiGroup"`
			Kind     string `json:"kind"`
			Name     string `json:"name"`
		} `json:"infrastructureRef"`
	} `json:"spec"`
	Status struct {
		NodeRef struct {
			Name string `json:"name"`
		} `json:"nodeRef"`
	} `json:"status"`
}

type machineDeploymentItem struct {
	Status struct {
		Replicas            int `json:"replicas"`
		ReadyReplicas       int `json:"readyReplicas"`
		UpdatedReplicas     int `json:"updatedReplicas"`
		UpToDateReplicas    int `json:"upToDateReplicas"`
		UnavailableReplicas int `json:"unavailableReplicas"`
	} `json:"status"`
}

type kubeadmControlPlaneItem struct {
	Status struct {
		Replicas            int `json:"replicas"`
		ReadyReplicas       int `json:"readyReplicas"`
		UpdatedReplicas     int `json:"updatedReplicas"`
		UpToDateReplicas    int `json:"upToDateReplicas"`
		UnavailableReplicas int `json:"unavailableReplicas"`
	} `json:"status"`
}

func expectCAPIMachinesWithProviderIDs(g Gomega, namespace, clusterName string, want int) {
	cmd := exec.Command("kubectl", "get", "machines", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name="+clusterName, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var list capiMachineList
	g.Expect(json.Unmarshal([]byte(output), &list)).To(Succeed())
	g.Expect(list.Items).To(HaveLen(want))
	for _, machine := range list.Items {
		g.Expect(machine.Spec.ProviderID).NotTo(BeNil(), "Machine %s has no providerID", machine.Metadata.Name)
		g.Expect(*machine.Spec.ProviderID).To(HavePrefix("stackit://"), "Machine %s has unexpected providerID", machine.Metadata.Name)
	}
}

func expectMachineDeploymentReadyReplicas(g Gomega, namespace, name string, replicas int) {
	machineDeployment := machineDeploymentForName(g, namespace, name)
	g.Expect(machineDeployment.Status.Replicas).To(Equal(replicas), "MachineDeployment %s has unexpected replicas", name)
	g.Expect(machineDeployment.Status.ReadyReplicas).To(Equal(replicas), "MachineDeployment %s has unexpected readyReplicas", name)
}

func expectMachineDeploymentRollout(g Gomega, namespace, name string, replicas int) {
	machineDeployment := machineDeploymentForName(g, namespace, name)
	g.Expect(machineDeployment.Status.Replicas).To(Equal(replicas), "MachineDeployment %s has unexpected replicas", name)
	g.Expect(currentMachineDeploymentReplicas(machineDeployment)).To(Equal(replicas), "MachineDeployment %s has unexpected updated/upToDate replicas", name)
	g.Expect(machineDeployment.Status.ReadyReplicas).To(Equal(replicas), "MachineDeployment %s has unexpected readyReplicas", name)
	g.Expect(machineDeployment.Status.UnavailableReplicas).To(BeZero(), "MachineDeployment %s has unavailable replicas", name)
}

func expectKubeadmControlPlaneReadyReplicas(g Gomega, namespace, name string, replicas int) {
	controlPlane := kubeadmControlPlaneForName(g, namespace, name)
	g.Expect(controlPlane.Status.Replicas).To(Equal(replicas), "KubeadmControlPlane %s has unexpected replicas", name)
	g.Expect(controlPlane.Status.ReadyReplicas).To(Equal(replicas), "KubeadmControlPlane %s has unexpected readyReplicas", name)
}

func expectKubeadmControlPlaneRollout(g Gomega, namespace, name string, replicas int) {
	controlPlane := kubeadmControlPlaneForName(g, namespace, name)
	g.Expect(controlPlane.Status.Replicas).To(Equal(replicas), "KubeadmControlPlane %s has unexpected replicas", name)
	g.Expect(currentKubeadmControlPlaneReplicas(controlPlane)).To(Equal(replicas), "KubeadmControlPlane %s has unexpected updated/upToDate replicas", name)
	g.Expect(controlPlane.Status.ReadyReplicas).To(Equal(replicas), "KubeadmControlPlane %s has unexpected readyReplicas", name)
	g.Expect(controlPlane.Status.UnavailableReplicas).To(BeZero(), "KubeadmControlPlane %s has unavailable replicas", name)
}

func machineDeploymentForName(g Gomega, namespace, name string) machineDeploymentItem {
	cmd := exec.Command("kubectl", "get", "machinedeployment", name, "-n", namespace, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var machineDeployment machineDeploymentItem
	g.Expect(json.Unmarshal([]byte(output), &machineDeployment)).To(Succeed())
	return machineDeployment
}

func kubeadmControlPlaneForName(g Gomega, namespace, name string) kubeadmControlPlaneItem {
	cmd := exec.Command("kubectl", "get", "kubeadmcontrolplane", name, "-n", namespace, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var controlPlane kubeadmControlPlaneItem
	g.Expect(json.Unmarshal([]byte(output), &controlPlane)).To(Succeed())
	return controlPlane
}

func expectMachinesVersion(g Gomega, machines []capiMachineItem, version string) {
	for _, machine := range machines {
		g.Expect(machine.Spec.Version).To(Equal(version), "Machine %s has unexpected Kubernetes version", machine.Metadata.Name)
	}
}

func currentMachineDeploymentReplicas(machineDeployment machineDeploymentItem) int {
	if machineDeployment.Status.UpdatedReplicas != 0 {
		return machineDeployment.Status.UpdatedReplicas
	}
	return machineDeployment.Status.UpToDateReplicas
}

func currentKubeadmControlPlaneReplicas(controlPlane kubeadmControlPlaneItem) int {
	if controlPlane.Status.UpdatedReplicas != 0 {
		return controlPlane.Status.UpdatedReplicas
	}
	return controlPlane.Status.UpToDateReplicas
}

func workerMachinesForDeployment(g Gomega, namespace, clusterName, deploymentName string) []capiMachineItem {
	machines := capiMachinesForCluster(g, namespace, clusterName)
	out := []capiMachineItem{}
	for _, machine := range machines {
		if machine.Metadata.Labels["cluster.x-k8s.io/deployment-name"] == deploymentName {
			out = append(out, machine)
		}
	}
	return out
}

func controlPlaneMachinesForCluster(g Gomega, namespace, clusterName string) []capiMachineItem {
	machines := capiMachinesForCluster(g, namespace, clusterName)
	out := []capiMachineItem{}
	for _, machine := range machines {
		if _, ok := machine.Metadata.Labels["cluster.x-k8s.io/control-plane"]; ok {
			out = append(out, machine)
		}
	}
	return out
}

type workloadNodeList struct {
	Items []workloadNodeItem `json:"items"`
}

type workloadNodeItem struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		ProviderID string `json:"providerID"`
		Taints     []struct {
			Key string `json:"key"`
		} `json:"taints"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func workloadKubeconfigFromSecret(g Gomega, clusterName, namespace string) string {
	cmd := exec.Command("kubectl", "get", "secret", clusterName+"-kubeconfig", "-n", namespace, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())

	var secret struct {
		Data map[string]string `json:"data"`
	}
	g.Expect(json.Unmarshal([]byte(output), &secret)).To(Succeed())
	encoded := secret.Data["value"]
	g.Expect(encoded).NotTo(BeEmpty(), "kubeconfig Secret %s-kubeconfig has no data.value", clusterName)
	kubeconfig, err := base64.StdEncoding.DecodeString(encoded)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to decode workload kubeconfig")

	path := filepath.Join(os.TempDir(), clusterName+".kubeconfig")
	g.Expect(os.WriteFile(path, kubeconfig, 0o600)).To(Succeed())
	return path
}

func kubectlOutputWithKubeconfig(g Gomega, kubeconfig string, args ...string) string {
	allArgs := append([]string{"--kubeconfig", kubeconfig}, args...)
	cmd := exec.Command("kubectl", allArgs...)
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(output)
}

func waitForKubeadmWorkloadClusterReady(opts workloadReadinessOptions, workloadKubeconfig *string) {
	By("waiting for the StackitCluster to become ready")
	Eventually(func(g Gomega) {
		if opts.Topology {
			expectTopologyStackitClusterReady(g, opts.Namespace, opts.ClusterName)
			return
		}
		expectNamedStackitClusterReady(g, opts.Namespace, opts.ClusterName)
	}, 15*time.Minute, 10*time.Second).Should(Succeed())

	if opts.Topology {
		By("waiting for the topology control-plane VM to provision")
		Eventually(func(g Gomega) {
			machine := readyTopologyControlPlaneStackitMachine(g, opts.Namespace, opts.ClusterName, opts.TestID)
			appendObservedInstanceIDs(opts.ObservedInstanceIDs, machine.Status.InstanceID)
		}, 45*time.Minute, 15*time.Second).Should(Succeed())
	} else {
		By("waiting for the control-plane VM to provision")
		Eventually(func(g Gomega) {
			machine := readyStackitMachineByNamePart(g, opts.Namespace, opts.TestID, "control-plane")
			appendObservedInstanceIDs(opts.ObservedInstanceIDs, machine.Status.InstanceID)
		}, 45*time.Minute, 15*time.Second).Should(Succeed())
	}

	By("extracting the workload Cluster kubeconfig")
	Eventually(func(g Gomega) {
		*workloadKubeconfig = workloadKubeconfigFromSecret(g, opts.ClusterName, opts.Namespace)
		output := kubectlOutputWithKubeconfig(g, *workloadKubeconfig, "get", "ns", "kube-system", "-o", "name")
		g.Expect(output).To(Equal("namespace/kube-system"))
	}, 20*time.Minute, 15*time.Second).Should(Succeed())

	if opts.InstallCNI {
		By("installing CNI into the workload Cluster")
		installWorkloadCNI(*workloadKubeconfig)
	}

	if opts.WaitForCCM {
		By("waiting for embedded cloud-provider-stackit rollout")
		waitForWorkloadCCMRollout(*workloadKubeconfig)
	}

	By("waiting for workload StackitMachines to provision")
	Eventually(func(g Gomega) {
		machines := readyStackitMachines(g, opts.Namespace, opts.TestID, opts.WantMachines)
		appendObservedInstanceIDs(opts.ObservedInstanceIDs, instanceIDs(machines)...)
	}, 45*time.Minute, 15*time.Second).Should(Succeed())

	By("waiting for workload Nodes to be Ready")
	Eventually(func(g Gomega) {
		expectWorkloadNodesReady(g, *workloadKubeconfig, opts.WantNodes)
	}, 25*time.Minute, 15*time.Second).Should(Succeed())

	By("verifying CAPI Machines reference Nodes with matching STACKIT providerIDs")
	Eventually(func(g Gomega) {
		expectProviderIDNodeRefAlignment(g, opts.Namespace, opts.ClusterName, opts.TestID, *workloadKubeconfig, opts.WantMachines)
	}, 15*time.Minute, 10*time.Second).Should(Succeed())
}

func appendObservedInstanceIDs(observed *[]string, instanceIDs ...string) {
	if observed == nil {
		return
	}
	*observed = appendUnique(*observed, instanceIDs...)
}

func waitForWorkloadCCMRollout(kubeconfig string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", "kube-system", "rollout", "status",
			"deployment/stackit-cloud-controller-manager", "--timeout=30s")
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, 5*time.Minute, 10*time.Second).Should(Succeed(), "embedded cloud-provider-stackit did not roll out")
}

func installWorkloadCNI(kubeconfig string) {
	if cniManifest := os.Getenv("STACKIT_E2E_CNI_MANIFEST"); cniManifest != "" {
		By("installing workload Cluster CNI from manifest")
		cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", cniManifest)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install workload Cluster CNI from %s", cniManifest)
		return
	}

	switch cni := strings.ToLower(envDefault("STACKIT_E2E_CNI", "cilium")); cni {
	case "cilium":
		installCiliumCNI(kubeconfig)
	case "calico":
		installManifestCNI(kubeconfig, "Calico", envDefault("STACKIT_E2E_CALICO_MANIFEST", "https://raw.githubusercontent.com/projectcalico/calico/v3.30.0/manifests/calico.yaml"))
	default:
		Fail(fmt.Sprintf("unsupported STACKIT_E2E_CNI %q; use cilium, calico, or STACKIT_E2E_CNI_MANIFEST", cni))
	}
}

func installCiliumCNI(kubeconfig string) {
	version := envDefault("STACKIT_E2E_CILIUM_VERSION", "1.19.4")
	args := []string{
		"install",
		"--kubeconfig", kubeconfig,
		"--version", version,
		"--set", "ipam.mode=cluster-pool",
		"--set", "ipam.operator.clusterPoolIPv4PodCIDRList=" + envDefault("STACKIT_E2E_CILIUM_CLUSTER_POOL_IPV4_CIDR", "192.168.0.0/16"),
		"--set", "ipam.operator.clusterPoolIPv4MaskSize=" + envDefault("STACKIT_E2E_CILIUM_CLUSTER_POOL_IPV4_MASK_SIZE", "24"),
	}
	if extraArgs := os.Getenv("STACKIT_E2E_CILIUM_INSTALL_ARGS"); extraArgs != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}

	By("installing Cilium into the workload Cluster")
	cmd := exec.Command("cilium", args...)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to install Cilium")

	By("waiting for Cilium rollout")
	for _, resource := range []string{"deployment/cilium-operator", "daemonset/cilium", "daemonset/cilium-envoy"} {
		cmd = exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", "kube-system", "rollout", "status", resource, "--timeout=10m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "%s did not roll out", resource)
	}
}

func installManifestCNI(kubeconfig, name, manifest string) {
	By(fmt.Sprintf("installing %s into the workload Cluster", name))
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to install %s from %s", name, manifest)
}

func renderCloudProviderStackitAddon(clusterName string, cfg stackitVMConfig, kubernetesVersion string, serviceAccountJSON []byte) string {
	addonBytes, err := os.ReadFile("templates/addons/cloud-provider-stackit.yaml")
	Expect(err).NotTo(HaveOccurred(), "Failed to read cloud-provider-stackit addon template")
	replacer := strings.NewReplacer(
		"${CLUSTER_NAME}", clusterName,
		"${STACKIT_PROJECT_ID}", cfg.ProjectID,
		"${STACKIT_REGION}", cfg.Region,
		"${STACKIT_NETWORK_ID}", cfg.NetworkID,
		"${STACKIT_SERVICE_ACCOUNT_JSON_B64}", base64.StdEncoding.EncodeToString(serviceAccountJSON),
		"${STACKIT_CLOUD_CONTROLLER_MANAGER_IMAGE}",
		cloudProviderStackitImageForKubernetesVersion(kubernetesVersion),
	)
	return replacer.Replace(string(addonBytes))
}

const externalCloudProviderTaint = "node.cloudprovider.kubernetes.io/uninitialized"

func expectWorkloadNodesReady(g Gomega, kubeconfig string, want int) {
	nodes := workloadNodes(g, kubeconfig)
	g.Expect(nodes).To(HaveLen(want))
	for _, node := range nodes {
		g.Expect(node.Spec.ProviderID).NotTo(BeEmpty(), "Node %s has no providerID", node.Metadata.Name)
		g.Expect(node.Spec.ProviderID).To(HavePrefix("stackit://"), "Node %s has unexpected providerID", node.Metadata.Name)
		g.Expect(nodeReady(node)).To(BeTrue(), "Node %s is not Ready", node.Metadata.Name)
		g.Expect(nodeHasTaint(node, externalCloudProviderTaint)).To(BeFalse(), "Node %s still has the external cloud-provider taint", node.Metadata.Name)
	}
}

func expectProviderIDNodeRefAlignment(g Gomega, namespace, clusterName, testID, kubeconfig string, want int) {
	machines := capiMachinesForCluster(g, namespace, clusterName)
	g.Expect(machines).To(HaveLen(want))

	stackitMachines := map[string]stackitMachineItem{}
	for _, machine := range stackitMachinesForTestID(g, namespace, testID) {
		stackitMachines[machine.Metadata.Name] = machine
	}
	nodes := map[string]workloadNodeItem{}
	for _, node := range workloadNodes(g, kubeconfig) {
		nodes[node.Metadata.Name] = node
	}

	for _, machine := range machines {
		g.Expect(machine.Spec.ProviderID).NotTo(BeNil(), "Machine %s has no providerID", machine.Metadata.Name)
		g.Expect(machine.Spec.InfrastructureRef.Name).NotTo(BeEmpty(), "Machine %s has no infrastructureRef.name", machine.Metadata.Name)
		stackitMachine, ok := stackitMachines[machine.Spec.InfrastructureRef.Name]
		g.Expect(ok).To(BeTrue(), "StackitMachine %s referenced by Machine %s not found", machine.Spec.InfrastructureRef.Name, machine.Metadata.Name)
		g.Expect(stackitMachine.Spec.ProviderID).NotTo(BeNil(), "StackitMachine %s has no spec providerID", stackitMachine.Metadata.Name)
		g.Expect(*stackitMachine.Spec.ProviderID).To(Equal(stackitMachine.Status.ProviderID), "StackitMachine %s spec/status providerID mismatch", stackitMachine.Metadata.Name)
		g.Expect(*machine.Spec.ProviderID).To(Equal(stackitMachine.Status.ProviderID), "Machine %s providerID does not match StackitMachine %s", machine.Metadata.Name, stackitMachine.Metadata.Name)

		g.Expect(machine.Status.NodeRef.Name).NotTo(BeEmpty(), "Machine %s has no status.nodeRef.name", machine.Metadata.Name)
		node, ok := nodes[machine.Status.NodeRef.Name]
		g.Expect(ok).To(BeTrue(), "Node %s referenced by Machine %s not found", machine.Status.NodeRef.Name, machine.Metadata.Name)
		g.Expect(node.Spec.ProviderID).To(Equal(stackitMachine.Status.ProviderID), "Node %s providerID does not match Machine %s", node.Metadata.Name, machine.Metadata.Name)
	}
}

func expectTopologyTemplateMetadata(g Gomega, namespace, clusterName, testID string) {
	const templateMetadataKey = "cluster-api-provider-stackit/template-metadata"

	cluster := topologyStackitCluster(g, namespace, clusterName)
	expectMetadataValue(g, "StackitCluster "+cluster.Metadata.Name, cluster.Metadata.Labels, cluster.Metadata.Annotations, templateMetadataKey, "cluster")

	machines := stackitMachinesForTestID(g, namespace, testID)
	var controlPlaneMachines, workerMachines []stackitMachineItem
	for _, machine := range machines {
		if machine.Metadata.Labels["cluster.x-k8s.io/cluster-name"] != clusterName {
			continue
		}
		if _, ok := machine.Metadata.Labels["cluster.x-k8s.io/control-plane"]; ok {
			controlPlaneMachines = append(controlPlaneMachines, machine)
			continue
		}
		workerMachines = append(workerMachines, machine)
	}

	g.Expect(controlPlaneMachines).To(HaveLen(1), "expected one topology control-plane StackitMachine for cluster %q", clusterName)
	for _, machine := range controlPlaneMachines {
		expectMetadataValue(g, "StackitMachine "+machine.Metadata.Name, machine.Metadata.Labels, machine.Metadata.Annotations, templateMetadataKey, "control-plane")
	}

	g.Expect(workerMachines).NotTo(BeEmpty(), "expected topology worker StackitMachines for cluster %q", clusterName)
	for _, machine := range workerMachines {
		expectMetadataValue(g, "StackitMachine "+machine.Metadata.Name, machine.Metadata.Labels, machine.Metadata.Annotations, templateMetadataKey, "worker")
	}
}

func expectMetadataValue(g Gomega, objectName string, labels, annotations map[string]string, key, want string) {
	g.Expect(labels).To(HaveKeyWithValue(key, want), "%s has unexpected propagated template label %q", objectName, key)
	g.Expect(annotations).To(HaveKeyWithValue(key, want), "%s has unexpected propagated template annotation %q", objectName, key)
}

func expectWorkloadNodesReadyForMachines(g Gomega, kubeconfig string, machines []capiMachineItem) {
	nodes := map[string]workloadNodeItem{}
	for _, node := range workloadNodes(g, kubeconfig) {
		nodes[node.Metadata.Name] = node
	}

	for _, machine := range machines {
		g.Expect(machine.Spec.ProviderID).NotTo(BeNil(), "Machine %s has no providerID", machine.Metadata.Name)
		g.Expect(machine.Status.NodeRef.Name).NotTo(BeEmpty(), "Machine %s has no status.nodeRef.name", machine.Metadata.Name)
		node, ok := nodes[machine.Status.NodeRef.Name]
		g.Expect(ok).To(BeTrue(), "Node %s referenced by Machine %s not found", machine.Status.NodeRef.Name, machine.Metadata.Name)
		g.Expect(node.Spec.ProviderID).To(Equal(*machine.Spec.ProviderID), "Node %s providerID does not match Machine %s", node.Metadata.Name, machine.Metadata.Name)
		g.Expect(nodeReady(node)).To(BeTrue(), "Node %s is not Ready", node.Metadata.Name)
		g.Expect(nodeHasTaint(node, externalCloudProviderTaint)).To(BeFalse(), "Node %s still has the external cloud-provider taint", node.Metadata.Name)
	}
}

func expectWorkloadNodesGone(g Gomega, kubeconfig string, nodeNames []string) {
	nodes := map[string]workloadNodeItem{}
	for _, node := range workloadNodes(g, kubeconfig) {
		nodes[node.Metadata.Name] = node
	}
	for _, nodeName := range nodeNames {
		_, ok := nodes[nodeName]
		g.Expect(ok).To(BeFalse(), "Node %s still exists", nodeName)
	}
}

func nodeNamesForMachines(machines []capiMachineItem) []string {
	out := make([]string, 0, len(machines))
	for _, machine := range machines {
		out = append(out, machine.Status.NodeRef.Name)
	}
	return out
}

func stackitInstanceIDsForMachines(g Gomega, namespace, testID string, machines []capiMachineItem) []string {
	stackitMachines := map[string]stackitMachineItem{}
	for _, machine := range stackitMachinesForTestID(g, namespace, testID) {
		stackitMachines[machine.Metadata.Name] = machine
	}

	out := make([]string, 0, len(machines))
	for _, machine := range machines {
		stackitMachine, ok := stackitMachines[machine.Spec.InfrastructureRef.Name]
		g.Expect(ok).To(BeTrue(), "StackitMachine %s referenced by Machine %s not found", machine.Spec.InfrastructureRef.Name, machine.Metadata.Name)
		g.Expect(stackitMachine.Status.InstanceID).NotTo(BeEmpty(), "StackitMachine %s has no instanceID", stackitMachine.Metadata.Name)
		out = append(out, stackitMachine.Status.InstanceID)
	}
	return out
}

func capiMachinesForCluster(g Gomega, namespace, clusterName string) []capiMachineItem {
	cmd := exec.Command("kubectl", "get", "machines", "-n", namespace, "-l", "cluster.x-k8s.io/cluster-name="+clusterName, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var list capiMachineList
	g.Expect(json.Unmarshal([]byte(output), &list)).To(Succeed())
	return list.Items
}

func workloadNodes(g Gomega, kubeconfig string) []workloadNodeItem {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var list workloadNodeList
	g.Expect(json.Unmarshal([]byte(output), &list)).To(Succeed())
	return list.Items
}

func nodeReady(node workloadNodeItem) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func nodeHasTaint(node workloadNodeItem, key string) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Key == key {
			return true
		}
	}
	return false
}

func readyStackitMachineInstanceIDs(g Gomega, namespace, testID string, want int) []string {
	return instanceIDs(readyStackitMachines(g, namespace, testID, want))
}

func readyStackitMachines(g Gomega, namespace, testID string, want int) []stackitMachineItem {
	machines := stackitMachinesForTestID(g, namespace, testID)
	g.Expect(machines).To(HaveLen(want))
	for _, machine := range machines {
		expectReadyStackitMachine(g, machine)
	}
	return machines
}

func readyTopologyControlPlaneStackitMachine(g Gomega, namespace, clusterName, testID string) stackitMachineItem {
	machines := stackitMachinesForTestID(g, namespace, testID)
	var matches []stackitMachineItem
	for _, machine := range machines {
		if machine.Metadata.Labels["cluster.x-k8s.io/cluster-name"] == clusterName {
			if _, ok := machine.Metadata.Labels["cluster.x-k8s.io/control-plane"]; ok {
				matches = append(matches, machine)
			}
		}
	}
	g.Expect(matches).To(HaveLen(1), "expected one topology control-plane StackitMachine for cluster %q", clusterName)
	expectReadyStackitMachine(g, matches[0])
	return matches[0]
}

func readyStackitMachineByNamePart(g Gomega, namespace, testID, namePart string) stackitMachineItem {
	machines := stackitMachinesForTestID(g, namespace, testID)
	var matches []stackitMachineItem
	for _, machine := range machines {
		if strings.Contains(machine.Metadata.Name, namePart) {
			matches = append(matches, machine)
		}
	}
	g.Expect(matches).To(HaveLen(1), "expected one StackitMachine containing %q", namePart)
	expectReadyStackitMachine(g, matches[0])
	return matches[0]
}

func expectReadyStackitMachine(g Gomega, machine stackitMachineItem) {
	g.Expect(machine.Status.Ready).To(BeTrue(), "StackitMachine %s is not ready", machine.Metadata.Name)
	g.Expect(machine.Status.InstanceID).NotTo(BeEmpty(), "StackitMachine %s has no instanceID", machine.Metadata.Name)
	g.Expect(machine.Status.ProviderID).To(Equal("stackit://"+machine.Status.InstanceID), "StackitMachine %s has unexpected status providerID", machine.Metadata.Name)
	g.Expect(machine.Spec.ProviderID).NotTo(BeNil(), "StackitMachine %s has no spec providerID", machine.Metadata.Name)
	g.Expect(*machine.Spec.ProviderID).To(Equal(machine.Status.ProviderID), "StackitMachine %s spec/status providerID mismatch", machine.Metadata.Name)
}

func instanceIDs(machines []stackitMachineItem) []string {
	out := make([]string, 0, len(machines))
	for _, machine := range machines {
		out = append(out, machine.Status.InstanceID)
	}
	return out
}

func stackitMachineNameForInstanceID(machines []stackitMachineItem, instanceID string) string {
	for _, machine := range machines {
		if machine.Status.InstanceID == instanceID {
			return machine.Metadata.Name
		}
	}
	Fail(fmt.Sprintf("No StackitMachine found for instanceID %s", instanceID))
	return ""
}

func difference(left, right []string) []string {
	rightSet := map[string]struct{}{}
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	out := []string{}
	for _, value := range left {
		if _, ok := rightSet[value]; !ok {
			out = append(out, value)
		}
	}
	return out
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func cleanupCloudServersByID(ctx context.Context, cloudClient cloud.Client, instanceIDs []string) {
	for _, instanceID := range instanceIDs {
		if err := cloudClient.DeleteServer(ctx, instanceID); err != nil && !cloud.IsNotFound(err) {
			_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API server cleanup warning for %s: %v\n", instanceID, err)
			continue
		}
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			_, err := cloudClient.GetServer(ctx, instanceID)
			if cloud.IsNotFound(err) {
				break
			}
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "STACKIT API server cleanup verification warning for %s: %v\n", instanceID, err)
				break
			}
			time.Sleep(15 * time.Second)
		}
	}
}

func cleanupStackitVMFixture(clusterName, machineName, namespace string) {
	for _, args := range [][]string{
		{"delete", "stackitmachine", machineName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=20m"},
		{"delete", "machine", machineName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=5m"},
		{"delete", "stackitcluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=5m"},
		{"delete", "cluster", clusterName, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=5m"},
		{"delete", "secret", machineName + "-bootstrap", "-n", namespace, "--ignore-not-found"},
	} {
		cmd := exec.Command("kubectl", args...)
		if _, err := utils.Run(cmd); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "cleanup warning: %v\n", err)
		}
	}
}

func writeTempManifest(pattern, content string) string {
	file, err := os.CreateTemp("", pattern)
	Expect(err).NotTo(HaveOccurred(), "Failed to create temporary manifest")
	defer func() {
		Expect(file.Close()).To(Succeed())
	}()
	_, err = file.WriteString(content)
	Expect(err).NotTo(HaveOccurred(), "Failed to write temporary manifest")
	return file.Name()
}

func renderTemplateFile(path string, replacements ...string) string {
	content, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred(), "Failed to read template %s", path)
	return strings.NewReplacer(replacements...).Replace(string(content))
}

func kubectlOutput(g Gomega, args ...string) string {
	cmd := exec.Command("kubectl", args...)
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(output)
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		Skip(fmt.Sprintf("%s is required for STACKIT VM e2e tests", name))
	}
	return value
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

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
