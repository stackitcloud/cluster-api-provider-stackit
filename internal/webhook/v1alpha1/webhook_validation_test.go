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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

const (
	validProjectID       = "11111111-1111-1111-1111-111111111111"
	validNetworkID       = "22222222-2222-2222-2222-222222222222"
	validImageID         = "33333333-3333-3333-3333-333333333333"
	validSecurityGroupID = "44444444-4444-4444-4444-444444444444"
	validServerID        = "55555555-5555-5555-5555-555555555555"
	updatedMachineType   = "c2i.4"
	testNamespace        = "default"
)

var _ = Describe("STACKIT admission webhooks", func() {
	Describe("StackitCluster", func() {
		var (
			validator StackitClusterCustomValidator
			defaulter StackitClusterCustomDefaulter
		)

		BeforeEach(func() {
			validator = StackitClusterCustomValidator{}
			defaulter = StackitClusterCustomDefaulter{}
		})

		It("accepts a valid minimal cluster", func() {
			_, err := validator.ValidateCreate(ctx, validStackitCluster("valid-cluster"))
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects a cluster without credentials secret name", func() {
			cluster := validStackitCluster("missing-credentials")
			cluster.Spec.CredentialsSecretRef.Name = ""

			_, err := validator.ValidateCreate(ctx, cluster)
			expectInvalidField(err, "spec.credentialsSecretRef.name")
		})

		It("rejects a disabled load balancer without control plane endpoint", func() {
			cluster := validStackitCluster("missing-endpoint")
			cluster.Spec.APIServerLoadBalancer.Enabled = false

			_, err := validator.ValidateCreate(ctx, cluster)
			expectInvalidField(err, "spec.controlPlaneEndpoint.host")
		})

		It("rejects an enabled bastion missing allowed CIDRs", func() {
			cluster := validStackitCluster("invalid-bastion")
			cluster.Spec.Bastion = validBastionSpec()
			cluster.Spec.Bastion.AllowedCIDRs = nil

			_, err := validator.ValidateCreate(ctx, cluster)
			expectInvalidField(err, "spec.bastion.allowedCIDRs")
		})

		It("accepts a valid bastion cloudInitRef", func() {
			cluster := validStackitCluster("valid-bastion")
			cluster.Spec.Bastion = validBastionSpec()
			cluster.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{
				Kind: "ConfigMap",
				Name: "bastion-cloud-init",
				Key:  "userData",
			}

			_, err := validator.ValidateCreate(ctx, cluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("defaults bastion root volume deleteOnTermination idempotently", func() {
			cluster := validStackitCluster("default-bastion")
			cluster.Spec.Bastion = validBastionSpec()

			Expect(defaulter.Default(ctx, cluster)).To(Succeed())
			Expect(cluster.Spec.Bastion.RootVolume.DeleteOnTermination).NotTo(BeNil())
			Expect(*cluster.Spec.Bastion.RootVolume.DeleteOnTermination).To(BeTrue())

			afterFirstDefault := cluster.DeepCopy()
			Expect(defaulter.Default(ctx, cluster)).To(Succeed())
			Expect(cluster).To(Equal(afterFirstDefault))
		})

		It("allows cloudInitRef updates after bastion provisioning", func() {
			oldCluster := validStackitCluster("cloudinit-update")
			oldCluster.Spec.Bastion = validBastionSpec()
			oldCluster.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{Kind: "ConfigMap", Name: "old-init", Key: "userData"}
			oldCluster.Status.Bastion.ServerID = validServerID
			newCluster := oldCluster.DeepCopy()
			newCluster.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{Kind: "ConfigMap", Name: "new-init", Key: "userData"}

			_, err := validator.ValidateUpdate(ctx, oldCluster, newCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects immutable cluster field updates", func() {
			oldCluster := validStackitCluster("immutable-cluster")
			newCluster := oldCluster.DeepCopy()
			newCluster.Spec.Network.ID = "66666666-6666-6666-6666-666666666666"

			_, err := validator.ValidateUpdate(ctx, oldCluster, newCluster)
			expectInvalidField(err, "spec.network.id")
		})
	})

	Describe("StackitMachine", func() {
		var (
			validator StackitMachineCustomValidator
			defaulter StackitMachineCustomDefaulter
		)

		BeforeEach(func() {
			validator = StackitMachineCustomValidator{}
			defaulter = StackitMachineCustomDefaulter{}
		})

		It("accepts a valid minimal machine", func() {
			_, err := validator.ValidateCreate(ctx, validStackitMachine("valid-machine"))
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects invalid providerID", func() {
			machine := validStackitMachine("invalid-providerid")
			providerID := "stackit://"
			machine.Spec.ProviderID = &providerID

			_, err := validator.ValidateCreate(ctx, machine)
			expectInvalidField(err, "spec.providerID")
		})

		It("defaults machine root volume deleteOnTermination idempotently", func() {
			machine := validStackitMachine("default-machine")

			Expect(defaulter.Default(ctx, machine)).To(Succeed())
			Expect(machine.Spec.RootVolume.DeleteOnTermination).NotTo(BeNil())
			Expect(*machine.Spec.RootVolume.DeleteOnTermination).To(BeTrue())

			afterFirstDefault := machine.DeepCopy()
			Expect(defaulter.Default(ctx, machine)).To(Succeed())
			Expect(machine).To(Equal(afterFirstDefault))
		})

		It("allows nil-to-valid providerID updates", func() {
			oldMachine := validStackitMachine("providerid-set")
			newMachine := oldMachine.DeepCopy()
			providerID := "stackit://" + validServerID
			newMachine.Spec.ProviderID = &providerID

			_, err := validator.ValidateUpdate(ctx, oldMachine, newMachine)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects providerID changes", func() {
			oldMachine := validStackitMachine("providerid-change")
			oldProviderID := "stackit://" + validServerID
			oldMachine.Spec.ProviderID = &oldProviderID
			newMachine := oldMachine.DeepCopy()
			newProviderID := "stackit://66666666-6666-6666-6666-666666666666"
			newMachine.Spec.ProviderID = &newProviderID

			_, err := validator.ValidateUpdate(ctx, oldMachine, newMachine)
			expectInvalidField(err, "spec.providerID")
		})

		It("rejects immutable machine creation field updates", func() {
			oldMachine := validStackitMachine("immutable-machine")
			newMachine := oldMachine.DeepCopy()
			newMachine.Spec.MachineType = updatedMachineType

			_, err := validator.ValidateUpdate(ctx, oldMachine, newMachine)
			expectInvalidField(err, "spec")
		})
	})

	Describe("StackitClusterTemplate", func() {
		var validator StackitClusterTemplateCustomValidator

		BeforeEach(func() {
			validator = StackitClusterTemplateCustomValidator{}
		})

		It("accepts valid template metadata", func() {
			template := validStackitClusterTemplate("valid-cluster-template")
			template.Spec.Template.ObjectMeta.Labels = map[string]string{"cluster-api-provider-stackit/template": "cluster"}
			template.Spec.Template.ObjectMeta.Annotations = map[string]string{"cluster-api-provider-stackit/template": "cluster"}

			_, err := validator.ValidateCreate(ctx, template)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects invalid template metadata", func() {
			template := validStackitClusterTemplate("invalid-cluster-template-metadata")
			template.Spec.Template.ObjectMeta.Labels = map[string]string{"not a label": "cluster"}

			_, err := validator.ValidateCreate(ctx, template)
			expectInvalidField(err, "spec.template.metadata.labels")
		})

		It("rejects template spec updates", func() {
			oldTemplate := validStackitClusterTemplate("immutable-cluster-template")
			newTemplate := oldTemplate.DeepCopy()
			newTemplate.Spec.Template.Spec.Region = "eu02"

			_, err := validator.ValidateUpdate(ctx, oldTemplate, newTemplate)
			expectInvalidField(err, "spec.template.spec")
			Expect(err.Error()).To(ContainSubstring("create a new template"))
		})
	})

	Describe("StackitMachineTemplate", func() {
		var (
			validator StackitMachineTemplateCustomValidator
			defaulter StackitMachineTemplateCustomDefaulter
		)

		BeforeEach(func() {
			validator = StackitMachineTemplateCustomValidator{}
			defaulter = StackitMachineTemplateCustomDefaulter{}
		})

		It("accepts valid template metadata", func() {
			template := validStackitMachineTemplate("valid-machine-template")
			template.Spec.Template.ObjectMeta.Labels = map[string]string{"cluster-api-provider-stackit/template": "machine"}
			template.Spec.Template.ObjectMeta.Annotations = map[string]string{"cluster-api-provider-stackit/template": "machine"}

			_, err := validator.ValidateCreate(ctx, template)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects invalid template metadata", func() {
			template := validStackitMachineTemplate("invalid-machine-template-metadata")
			template.Spec.Template.ObjectMeta.Annotations = map[string]string{"not an annotation": "machine"}

			_, err := validator.ValidateCreate(ctx, template)
			expectInvalidField(err, "spec.template.metadata.annotations")
		})

		It("rejects providerID in templates", func() {
			template := validStackitMachineTemplate("providerid-template")
			providerID := "stackit://" + validServerID
			template.Spec.Template.Spec.ProviderID = &providerID

			_, err := validator.ValidateCreate(ctx, template)
			expectInvalidField(err, "spec.template.spec.providerID")
		})

		It("defaults template root volume like machine root volume", func() {
			template := validStackitMachineTemplate("default-machine-template")

			Expect(defaulter.Default(ctx, template)).To(Succeed())
			Expect(template.Spec.Template.Spec.RootVolume.DeleteOnTermination).NotTo(BeNil())
			Expect(*template.Spec.Template.Spec.RootVolume.DeleteOnTermination).To(BeTrue())
		})

		It("rejects template spec updates", func() {
			oldTemplate := validStackitMachineTemplate("immutable-machine-template")
			newTemplate := oldTemplate.DeepCopy()
			newTemplate.Spec.Template.Spec.MachineType = updatedMachineType

			_, err := validator.ValidateUpdate(ctx, oldTemplate, newTemplate)
			expectInvalidField(err, "spec.template.spec")
			Expect(err.Error()).To(ContainSubstring("create a new template"))
		})
	})

	Describe("live admission", func() {
		It("defaults machine template root volume through the API server", func() {
			template := validStackitMachineTemplate("live-default-machine-template")
			template.Namespace = testNamespace

			Expect(k8sClient.Create(ctx, template)).To(Succeed())

			got := &infrav1.StackitMachineTemplate{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(template), got)).To(Succeed())
			Expect(got.Spec.Template.Spec.RootVolume.DeleteOnTermination).NotTo(BeNil())
			Expect(*got.Spec.Template.Spec.RootVolume.DeleteOnTermination).To(BeTrue())
		})

		It("rejects providerID in machine templates through the API server", func() {
			template := validStackitMachineTemplate("live-providerid-machine-template")
			template.Namespace = testNamespace
			providerID := "stackit://" + validServerID
			template.Spec.Template.Spec.ProviderID = &providerID

			err := k8sClient.Create(ctx, template)
			expectInvalidField(err, "spec.template.spec.providerID")
		})

		It("rejects machine template spec updates through the API server", func() {
			template := validStackitMachineTemplate("live-immutable-machine-template")
			template.Namespace = testNamespace
			Expect(k8sClient.Create(ctx, template)).To(Succeed())

			template.Spec.Template.Spec.MachineType = updatedMachineType
			err := k8sClient.Update(ctx, template)
			expectInvalidField(err, "spec.template.spec")
		})
	})
})

func validStackitCluster(name string) *infrav1.StackitCluster {
	return &infrav1.StackitCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: infrav1.StackitClusterSpec{
			ProjectID: validProjectID,
			Region:    "eu01",
			CredentialsSecretRef: coreSecretReference(
				"stackit-credentials",
			),
			Network: infrav1.StackitClusterNetworkSpec{
				ID: validNetworkID,
			},
			APIServerLoadBalancer: infrav1.StackitAPIServerLoadBalancerSpec{
				Enabled: true,
			},
		},
	}
}

func validStackitMachine(name string) *infrav1.StackitMachine {
	return &infrav1.StackitMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: validStackitMachineSpec(),
	}
}

func validStackitClusterTemplate(name string) *infrav1.StackitClusterTemplate {
	return &infrav1.StackitClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: infrav1.StackitClusterTemplateSpec{
			Template: infrav1.StackitClusterTemplateResource{
				Spec: validStackitCluster(name).Spec,
			},
		},
	}
}

func validStackitMachineTemplate(name string) *infrav1.StackitMachineTemplate {
	return &infrav1.StackitMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: infrav1.StackitMachineTemplateSpec{
			Template: infrav1.StackitMachineTemplateResource{
				Spec: validStackitMachineSpec(),
			},
		},
	}
}

func validStackitMachineSpec() infrav1.StackitMachineSpec {
	return infrav1.StackitMachineSpec{
		ImageID:     validImageID,
		MachineType: "c2i.2",
		RootVolume: infrav1.StackitRootVolumeSpec{
			SizeGiB:          50,
			PerformanceClass: "storage_premium_perf6",
		},
		Network: infrav1.StackitMachineNetworkSpec{
			ID: validNetworkID,
		},
		SecurityGroups: []string{validSecurityGroupID},
	}
}

func validBastionSpec() infrav1.StackitBastionSpec {
	return infrav1.StackitBastionSpec{
		Enabled:      true,
		ImageID:      validImageID,
		MachineType:  "c2i.2",
		SSHKeyName:   "cluster-api-provider-stackit",
		AllowedCIDRs: []string{"203.0.113.0/24"},
		RootVolume: infrav1.StackitRootVolumeSpec{
			SizeGiB:          20,
			PerformanceClass: "storage_premium_perf6",
		},
	}
}

func coreSecretReference(name string) corev1.SecretReference {
	return corev1.SecretReference{Name: name}
}

func expectInvalidField(err error, field string) {
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected invalid error, got %T: %v", err, err)
	statusErr := err.(*apierrors.StatusError)
	Expect(statusErr.ErrStatus.Details).NotTo(BeNil())
	for _, cause := range statusErr.ErrStatus.Details.Causes {
		if cause.Field == field {
			return
		}
	}
	Fail("expected invalid field " + field + " in " + err.Error())
}
