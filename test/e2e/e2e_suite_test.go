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
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stackitcloud/cluster-api-provider-stackit/test/utils"
)

var (
	// managerImage is the manager image to be built and loaded for testing.
	managerImage = envDefault("E2E_MANAGER_IMAGE", "example.com/cluster-api-provider-stackit:v0.0.1")
	// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
	shouldCleanupCertManager = false
)

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind and CertManager.
//
// To enable kubectl kuberc (use custom kubectl configurations), set: KUBECTL_KUBERC=true
// By default, kuberc is disabled to ensure consistent test behavior across different environments.
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting cluster-api-provider-stackit e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	// TODO(user): If you want to change the e2e test vendor from Kind,
	// ensure the image is built and available, then remove the following block.
	By("loading the manager image on Kind")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	configureKubectlKubeRC()
	setupCertManager()
	setupClusterAPI()
})

var _ = AfterSuite(func() {
	teardownCertManager()
})

// Disable kubectl kuberc by default for test isolation.
// This prevents local kubectl configurations from affecting test behavior.
// To enable kuberc, set: KUBECTL_KUBERC=true
func configureKubectlKubeRC() {
	if os.Getenv("KUBECTL_KUBERC") != "true" {
		By("disabling kubectl kuberc for test isolation")
		err := os.Setenv("KUBECTL_KUBERC", "false")
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to disable kubectl kuberc")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl kuberc disabled for consistent test behavior (override with KUBECTL_KUBERC=true)\n")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl kuberc enabled (KUBECTL_KUBERC=true)\n")
	}
}

// setupCertManager installs CertManager if needed for webhook tests.
// Skips installation if CERT_MANAGER_INSTALL_SKIP=true or if already present.
func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation (CERT_MANAGER_INSTALL_SKIP=true)\n")
		return
	}

	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	// Mark for cleanup before installation to handle interruptions and partial installs.
	shouldCleanupCertManager = true

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

func setupClusterAPI() {
	By("installing Cluster API providers")
	cmd := exec.Command("clusterctl", "init", "--core", "cluster-api", "--bootstrap", "kubeadm", "--control-plane", "kubeadm")
	cmd.Env = append(os.Environ(), "CLUSTER_RESOURCE_SET=true", "CLUSTER_TOPOLOGY=true")
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install Cluster API providers")

	ensureClusterTopologyFeatureGate("capi-controller-manager", "capi-system")
	ensureClusterTopologyFeatureGate("capi-kubeadm-control-plane-controller-manager", "capi-kubeadm-control-plane-system")
	waitForDeploymentAvailable("capi-controller-manager", "capi-system")
	waitForDeploymentAvailable("capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-bootstrap-system")
	waitForDeploymentAvailable("capi-kubeadm-control-plane-controller-manager", "capi-kubeadm-control-plane-system")
}

func ensureClusterTopologyFeatureGate(name, namespace string) {
	By(fmt.Sprintf("ensuring %s/%s has ClusterTopology enabled", namespace, name))
	cmd := exec.Command("kubectl", "-n", namespace, "get", "deployment", name, "-o", "json")
	output, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to inspect deployment %s/%s", namespace, name)

	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string   `json:"name"`
						Args []string `json:"args"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	ExpectWithOffset(1, json.Unmarshal([]byte(output), &deployment)).To(Succeed())
	ExpectWithOffset(1, deployment.Spec.Template.Spec.Containers).NotTo(BeEmpty(), "Deployment %s/%s has no containers", namespace, name)

	args := append([]string(nil), deployment.Spec.Template.Spec.Containers[0].Args...)
	changed := false
	for i, arg := range args {
		if !strings.HasPrefix(arg, "--feature-gates=") {
			continue
		}
		if strings.Contains(arg, "ClusterTopology=false") {
			args[i] = strings.ReplaceAll(arg, "ClusterTopology=false", "ClusterTopology=true")
			changed = true
		} else if !strings.Contains(arg, "ClusterTopology=true") {
			args[i] = arg + ",ClusterTopology=true"
			changed = true
		}
	}
	if !changed {
		return
	}

	containerName := deployment.Spec.Template.Spec.Containers[0].Name
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name": containerName,
							"args": args,
						},
					},
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	cmd = exec.Command("kubectl", "-n", namespace, "patch", "deployment", name, "--type=strategic", "-p", string(patchBytes))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to enable ClusterTopology for deployment %s/%s", namespace, name)
}

func waitForDeploymentAvailable(name, namespace string) {
	By(fmt.Sprintf("waiting for %s/%s to become available", namespace, name))
	cmd := exec.Command("kubectl", "wait", "deployment.apps/"+name,
		"--for", "condition=Available",
		"--namespace", namespace,
		"--timeout", "5m",
	)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Deployment %s/%s did not become available", namespace, name)
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by this suite)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
