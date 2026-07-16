/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud/fake"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

var _ = Describe("StackitMachine Controller", func() {
	const namespace = "default"

	var (
		ctx           context.Context
		clusterName   string
		machineName   string
		stackitName   string
		credentials   string
		fakeCloud     *cloudfake.Client
		reconciler    *StackitMachineReconciler
		request       reconcile.Request
		stackitKey    types.NamespacedName
		stackitMach   *infrav1.StackitMachine
		bootstrapName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		suffix := time.Now().UnixNano()
		clusterName = fmt.Sprintf("cluster-%d", suffix)
		machineName = fmt.Sprintf("machine-%d", suffix)
		stackitName = fmt.Sprintf("stackit-machine-%d", suffix)
		credentials = "stackit-credentials-" + fmt.Sprint(suffix)
		bootstrapName = fmt.Sprintf("bootstrap-%d", suffix)
		stackitKey = types.NamespacedName{Namespace: namespace, Name: stackitName}
		request = reconcile.Request{NamespacedName: stackitKey}
		fakeCloud = cloudfake.New(cloud.Network{ID: testNetworkID, Name: "network"})
		reconciler = &StackitMachineReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			CloudClientFactory: func(context.Context, cloud.Credentials) (cloud.Client, error) {
				return fakeCloud, nil
			},
		}

		createCredentialsSecret(ctx, credentials, namespace, testProjectID)
		createOwnerCluster(ctx, clusterName, namespace)
		createReadyStackitCluster(ctx, clusterName, namespace, credentials)
		createOwnerMachine(ctx, machineName, namespace, clusterName, stackitName, nil)
		stackitMach = newStackitMachine(stackitName, namespace, machineName)
		Expect(k8sClient.Create(ctx, stackitMach)).To(Succeed())
	})

	AfterEach(func() {
		deleteIfExists(ctx, stackitMach)
		deleteIfExists(ctx, &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: machineName, Namespace: namespace}})
		deleteIfExists(ctx, &infrav1.StackitCluster{ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace}})
		deleteIfExists(ctx, &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace}})
		deleteIfExists(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credentials, Namespace: namespace}})
		deleteIfExists(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bootstrapName, Namespace: namespace}})
	})

	It("does not create a VM when Machine.spec.bootstrap.dataSecretName is empty", func() {
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeFalse())
		expectCondition(got.Status.Conditions, infrav1.MachineBootstrapReadyCondition, metav1.ConditionFalse, "BootstrapDataSecretMissing")
	})

	It("does not create a VM when the bootstrap Secret is missing", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.MachineBootstrapReadyCondition, metav1.ConditionFalse, "BootstrapDataSecretNotFound")
	})

	It("creates a VM and sets provider status when bootstrap data is available", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(fakeCloud.ServerCount()).To(Equal(1))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeTrue())
		Expect(got.Status.InstanceID).NotTo(BeEmpty())
		Expect(got.Status.InstanceState).To(Equal("ACTIVE"))
		Expect(got.Status.ProviderID).To(Equal("stackit://" + got.Status.InstanceID))
		Expect(got.Spec.ProviderID).NotTo(BeNil())
		Expect(*got.Spec.ProviderID).To(Equal(got.Status.ProviderID))
		Expect(got.Status.Addresses).To(HaveLen(1))
		expectCondition(got.Status.Conditions, infrav1.MachineReadyCondition, metav1.ConditionTrue, "Available")
		expectCondition(got.Status.Conditions, infrav1.MachineBootstrapReadyCondition, metav1.ConditionTrue, "Available")
		expectCondition(got.Status.Conditions, infrav1.MachineInstanceReadyCondition, metav1.ConditionTrue, "Available")
	})

	It("attaches provider-managed node SSH access when bastion is enabled", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		stackitCluster := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, stackitCluster)).To(Succeed())
		stackitCluster.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Update(ctx, stackitCluster)).To(Succeed())
		stackitCluster.Status.Ready = true
		stackitCluster.Status.Bastion.SecurityGroupID = "bastion-sg"
		Expect(k8sClient.Status().Update(ctx, stackitCluster)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeTrue())
		Expect(fakeCloud.EnsureNodeSSHCalls).To(Equal(1))
		securityGroups, err := fakeCloud.ListSecurityGroupsByTags(ctx, map[string]string{
			util.LabelClusterName:  clusterName,
			util.LabelResourceRole: util.ResourceRoleNodeSSH,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(securityGroups).To(HaveLen(1))
		Expect(fakeCloud.SecurityGroupRemoteSources(securityGroups[0].ID)).To(ConsistOf("bastion-sg"))
		Expect(fakeCloud.ServerHasSecurityGroup(got.Status.InstanceID, securityGroups[0].ID)).To(BeTrue())
	})

	It("does not create a VM when availabilityZone is outside published failure domains", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.AvailabilityZone = "eu01-9"
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeFalse())
		expectCondition(got.Status.Conditions, infrav1.MachineInstanceReadyCondition, metav1.ConditionFalse, "InvalidFailureDomain")
		expectCondition(got.Status.Conditions, infrav1.MachineReadyCondition, metav1.ConditionFalse, "InvalidFailureDomain")
	})

	It("marks credentials invalid without requeueing on unauthorized credentials", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		reconciler.CloudClientFactory = func(context.Context, cloud.Credentials) (cloud.Client, error) {
			return nil, fmt.Errorf("authenticate: %w", cloud.ErrUnauthorized)
		}

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.MachineCredentialsReadyCondition, metav1.ConditionFalse, "CredentialsInvalid")
		expectCondition(got.Status.Conditions, infrav1.MachineReadyCondition, metav1.ConditionFalse, "CredentialsInvalid")
	})

	It("does not call the cloud API when the owning Cluster is paused", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		cluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster)).To(Succeed())
		cluster.Spec.Paused = ptr.To(true)
		Expect(k8sClient.Update(ctx, cluster)).To(Succeed())

		cloudClientFactoryCalls := 0
		reconciler.CloudClientFactory = func(context.Context, cloud.Credentials) (cloud.Client, error) {
			cloudClientFactoryCalls++
			return fakeCloud, nil
		}

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(cloudClientFactoryCalls).To(Equal(0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)
	})

	It("does not call the cloud API when the StackitMachine has the paused annotation", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Annotations = map[string]string{clusterv1.PausedAnnotation: ""}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		cloudClientFactoryCalls := 0
		reconciler.CloudClientFactory = func(context.Context, cloud.Credentials) (cloud.Client, error) {
			cloudClientFactoryCalls++
			return fakeCloud, nil
		}

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(cloudClientFactoryCalls).To(Equal(0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)
	})

	It("requeues when server lookup returns a conflict", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		fakeCloud.FailNextFindServer = fmt.Errorf("multiple matching servers: %w", cloud.ErrConflict)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.MachineInstanceReadyCondition, metav1.ConditionFalse, "InstanceError")
		expectCondition(got.Status.Conditions, infrav1.MachineReadyCondition, metav1.ConditionFalse, "InstanceError")
	})

	It("requeues when VM creation returns a transient error", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		createBootstrapSecret(ctx, bootstrapName)
		fakeCloud.FailNextCreateServer = fmt.Errorf("create server timeout: %w", cloud.ErrTransient)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.MachineInstanceReadyCondition, metav1.ConditionFalse, "InstanceError")
		expectCondition(got.Status.Conditions, infrav1.MachineReadyCondition, metav1.ConditionFalse, "InstanceError")
	})

	It("registers control plane VMs as API server load balancer targets", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		updateMachineControlPlaneLabel(ctx, machineName, namespace)
		enableStackitClusterLoadBalancer(ctx, clusterName, namespace)
		createBootstrapSecret(ctx, bootstrapName)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(fakeCloud.ServerCount()).To(Equal(1))
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(1))

		stackitCluster := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, stackitCluster)).To(Succeed())
		loadBalancerID := stackitCluster.Status.APIServerLoadBalancerID
		Expect(loadBalancerID).NotTo(BeEmpty())
		Expect(fakeCloud.LoadBalancerTargetCount(loadBalancerID)).To(Equal(1))
	})

	It("requeues when load balancer target registration returns a transient error", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		updateMachineControlPlaneLabel(ctx, machineName, namespace)
		createBootstrapSecret(ctx, bootstrapName)
		loadBalancerID := createAPIServerLoadBalancer(ctx, fakeCloud)
		updateStackitClusterLoadBalancer(ctx, clusterName, namespace, loadBalancerID)
		fakeCloud.FailNextEnsureTarget = fmt.Errorf("update target pool timeout: %w", cloud.ErrTransient)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.MachineReadyCondition, metav1.ConditionFalse, "LoadBalancerTargetError")
	})

	It("deletes the VM and removes the finalizer", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		updateMachineControlPlaneLabel(ctx, machineName, namespace)
		enableStackitClusterLoadBalancer(ctx, clusterName, namespace)
		createBootstrapSecret(ctx, bootstrapName)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.ServerCount()).To(Equal(1))

		stackitCluster := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, stackitCluster)).To(Succeed())
		loadBalancerID := stackitCluster.Status.APIServerLoadBalancerID
		Expect(loadBalancerID).NotTo(BeEmpty())
		Expect(fakeCloud.LoadBalancerTargetCount(loadBalancerID)).To(Equal(1))

		got := &infrav1.StackitMachine{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.ServerCount()).To(Equal(0))
		Expect(fakeCloud.LoadBalancerTargetCount(loadBalancerID)).To(Equal(0))
		Eventually(func() bool {
			err := k8sClient.Get(ctx, stackitKey, &infrav1.StackitMachine{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("maps owning Machine events to StackitMachine reconcile requests", func() {
		machine := &clusterv1.Machine{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: machineName, Namespace: namespace}, machine)).To(Succeed())

		requests := reconciler.stackitMachineRequestsForMachine(ctx, machine)
		Expect(requests).To(Equal([]reconcile.Request{request}))
	})

	It("maps related StackitCluster events to StackitMachine reconcile requests", func() {
		stackitCluster := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, stackitCluster)).To(Succeed())

		requests := reconciler.stackitMachineRequestsForStackitCluster(ctx, stackitCluster)
		Expect(requests).To(ConsistOf(request))
	})

	It("maps bootstrap Secret events to StackitMachine reconcile requests", func() {
		updateMachineBootstrapSecret(ctx, machineName, bootstrapName)
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bootstrapName, Namespace: namespace}}

		requests := reconciler.stackitMachineRequestsForBootstrapSecret(ctx, secret)
		Expect(requests).To(ConsistOf(request))
	})

	It("ignores Machine events for other infrastructure providers", func() {
		machine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: namespace},
			Spec: clusterv1.MachineSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: "other.infrastructure.example.com",
					Kind:     "OtherMachine",
					Name:     "other",
				},
			},
		}

		requests := reconciler.stackitMachineRequestsForMachine(ctx, machine)
		Expect(requests).To(BeEmpty())
	})
})

func newStackitMachine(name, namespace, machineName string) *infrav1.StackitMachine {
	return &infrav1.StackitMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Machine",
				Name:       machineName,
				UID:        types.UID("machine-" + machineName),
			}},
		},
		Spec: infrav1.StackitMachineSpec{
			ImageID:          testImageID,
			MachineType:      "c2i.2",
			AvailabilityZone: "eu01-1",
			Network: infrav1.StackitMachineNetworkSpec{
				ID: testNetworkID,
			},
		},
	}
}
