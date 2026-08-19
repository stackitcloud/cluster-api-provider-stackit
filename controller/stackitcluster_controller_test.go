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
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	cloudfake "github.com/stackitcloud/cluster-api-provider-stackit/cloud/fake"
	bastionservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
)

var _ = Describe("StackitCluster Controller", func() {
	const namespace = "default"

	var (
		ctx          context.Context
		clusterName  string
		credentials  string
		fakeCloud    *cloudfake.Client
		reconciler   *StackitClusterReconciler
		request      reconcile.Request
		stackitKey   types.NamespacedName
		stackitClust *infrav1.StackitCluster
	)

	BeforeEach(func() {
		ctx = context.Background()
		clusterName = fmt.Sprintf("cluster-%d", time.Now().UnixNano())
		credentials = "stackit-credentials-" + clusterName
		stackitKey = types.NamespacedName{Namespace: namespace, Name: clusterName}
		request = reconcile.Request{NamespacedName: stackitKey}
		fakeCloud = cloudfake.New(cloud.Network{ID: testNetworkID, Name: "network"})
		reconciler = &StackitClusterReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			CloudClientFactory: func(context.Context, cloud.Credentials) (cloud.Client, error) {
				return fakeCloud, nil
			},
		}

		createCredentialsSecret(ctx, credentials, namespace, testProjectID)
		createOwnerCluster(ctx, clusterName)
		stackitClust = newStackitCluster(clusterName, namespace, true)
		stackitClust.Spec.CredentialsSecretRef.Name = credentials
		Expect(k8sClient.Create(ctx, stackitClust)).To(Succeed())
	})

	AfterEach(func() {
		deleteIfExists(ctx, stackitClust)
		deleteIfExists(ctx, &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace}})
		deleteIfExists(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credentials, Namespace: namespace}})
	})

	It("creates the API server load balancer and publishes the control plane endpoint", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeTrue())
		Expect(got.Spec.ControlPlaneEndpoint).To(Equal(clusterv1.APIEndpoint{Host: "203.0.113.10", Port: 6443}))
		Expect(got.Status.APIServerEndpoint).To(Equal(got.Spec.ControlPlaneEndpoint))
		Expect(got.Status.APIServerLoadBalancerID).NotTo(BeEmpty())
		Expect(got.Status.FailureDomains).To(ConsistOf(
			clusterv1.FailureDomain{Name: "eu01-1", ControlPlane: new(true), Attributes: map[string]string{"region": "eu01"}},
			clusterv1.FailureDomain{Name: "eu01-2", ControlPlane: new(true), Attributes: map[string]string{"region": "eu01"}},
			clusterv1.FailureDomain{Name: "eu01-3", ControlPlane: new(true), Attributes: map[string]string{"region": "eu01"}},
		))
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(1))
		expectCondition(got.Status.Conditions, infrav1.ClusterReadyCondition, metav1.ConditionTrue, "Available")
		expectCondition(got.Status.Conditions, infrav1.ClusterNetworkReadyCondition, metav1.ConditionTrue, "Available")
		expectCondition(got.Status.Conditions, infrav1.ClusterLoadBalancerReadyCondition, metav1.ConditionTrue, "Available")
	})

	It("uses an externally provided control plane endpoint when load balancer creation is disabled", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.APIServerLoadBalancer.Enabled = false
		got.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "198.51.100.10", Port: 6443}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeTrue())
		Expect(got.Status.APIServerEndpoint.Host).To(Equal("198.51.100.10"))
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(0))
		expectCondition(got.Status.Conditions, infrav1.ClusterLoadBalancerReadyCondition, metav1.ConditionTrue, "Skipped")
	})

	It("creates the bastion and publishes its public IP when enabled", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeTrue())
		Expect(got.Status.Bastion.ServerID).NotTo(BeEmpty())
		Expect(got.Status.Bastion.PublicIPID).NotTo(BeEmpty())
		Expect(got.Status.Bastion.PublicIP).To(Equal("203.0.113.22"))
		Expect(got.Status.Bastion.SecurityGroupID).NotTo(BeEmpty())
		Expect(fakeCloud.ServerCount()).To(Equal(1))
		Expect(fakeCloud.PublicIPCount()).To(Equal(1))
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(1))
		expectCondition(got.Status.Conditions, infrav1.ClusterBastionReadyCondition, metav1.ConditionTrue, "Available")
	})

	It("deletes existing bastion resources when bastion is disabled", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		_, err = fakeCloud.EnsureNodeSSHAccess(ctx, cloud.NodeSSHAccessInput{
			Name:                   got.Name + "-node-ssh",
			ServerID:               got.Status.Bastion.ServerID,
			BastionSecurityGroupID: got.Status.Bastion.SecurityGroupID,
			Tags:                   bastionservice.NodeSSHAccessTags(got),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(2))
		got.Spec.Bastion.Enabled = false
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Bastion).To(Equal(infrav1.StackitBastionStatus{}))
		Expect(fakeCloud.ServerCount()).To(Equal(0))
		Expect(fakeCloud.PublicIPCount()).To(Equal(0))
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(0))
		expectCondition(got.Status.Conditions, infrav1.ClusterBastionReadyCondition, metav1.ConditionTrue, "Skipped")
	})

	It("deletes bastion resources during cluster deletion", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Bastion.PublicIP).NotTo(BeEmpty())
		_, err = fakeCloud.EnsureNodeSSHAccess(ctx, cloud.NodeSSHAccessInput{
			Name:                   got.Name + "-node-ssh",
			ServerID:               got.Status.Bastion.ServerID,
			BastionSecurityGroupID: got.Status.Bastion.SecurityGroupID,
			Tags:                   bastionservice.NodeSSHAccessTags(got),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(2))
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeCloud.ServerCount()).To(Equal(0))
		Expect(fakeCloud.PublicIPCount()).To(Equal(0))
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(0))
		Eventually(func() bool {
			err := k8sClient.Get(ctx, stackitKey, &infrav1.StackitCluster{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("cleans up bastion resources during deletion even when bastion status was never persisted", func() {
		// Regression test for debug/deletion-bug.md: the cloud-cleanup block used
		// to be gated on persisted status for the bastion, while the load
		// balancer was gated on its spec flag. A bastion created without its
		// status patch landing (process restart, conflict) therefore skipped
		// cleanup entirely and leaked server, public IP and security group.
		createOwnerCluster(ctx, clusterName+"-nolb")
		defer deleteIfExists(ctx, &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-nolb", Namespace: namespace},
		})
		lbDisabled := newStackitCluster(clusterName+"-nolb", namespace, false)
		lbDisabled.Spec.CredentialsSecretRef.Name = credentials
		lbDisabled.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Create(ctx, lbDisabled)).To(Succeed())
		defer deleteIfExists(ctx, lbDisabled)

		key := types.NamespacedName{Namespace: namespace, Name: lbDisabled.Name}
		req := reconcile.Request{NamespacedName: key}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Bastion.ServerID).NotTo(BeEmpty())
		Expect(fakeCloud.ServerCount()).To(Equal(1))

		By("losing the persisted bastion status, as if the patch never landed")
		got.Status.Bastion = infrav1.StackitBastionStatus{}
		Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(hasBastionStatus(got.Status.Bastion)).To(BeFalse())
		Expect(got.Status.APIServerLoadBalancerID).To(BeEmpty())

		By("deleting the cluster")
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeCloud.ServerCount()).To(Equal(0),
			"bastion server leaked because cleanup was gated on status alone")
		Expect(fakeCloud.PublicIPCount()).To(Equal(0))
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(0))
	})

	It("finalizes deletion when the credentials Secret is already gone", func() {
		// A missing credentials Secret cannot be recovered from, and it commonly
		// disappears first during namespace teardown. Broadening the delete gate
		// to spec.Bastion.Enabled made a working cloud client mandatory for every
		// bastion cluster, which would strand such a cluster in Terminating.
		createOwnerCluster(ctx, clusterName+"-nocreds")
		defer deleteIfExists(ctx, &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-nocreds", Namespace: namespace},
		})
		orphaned := newStackitCluster(clusterName+"-nocreds", namespace, false)
		orphaned.Spec.CredentialsSecretRef.Name = credentials
		orphaned.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Create(ctx, orphaned)).To(Succeed())
		defer deleteIfExists(ctx, orphaned)

		key := types.NamespacedName{Namespace: namespace, Name: orphaned.Name}
		req := reconcile.Request{NamespacedName: key}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		By("removing the credentials Secret, as namespace teardown would")
		Expect(k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: credentials, Namespace: namespace},
		})).To(Succeed())

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred(), "deletion must not block on a Secret that can never come back")

		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &infrav1.StackitCluster{}))
		}).Should(BeTrue(), "cluster stayed in Terminating because the finalizer was never removed")
	})

	It("tears the bastion down when disabled even if its status was never persisted", func() {
		// Counterpart to the deletion path: disabling the bastion used to be
		// gated on hasBastionStatus alone. With the status lost, nothing was torn
		// down while the condition reported "bastion disabled" — leaving port 22
		// open for the rest of the cluster's life.
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.ServerCount()).To(Equal(1))

		By("losing the persisted bastion status and its condition")
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Status.Bastion = infrav1.StackitBastionStatus{}
		got.Status.Conditions = nil
		Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

		By("disabling the bastion")
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion.Enabled = false
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeCloud.ServerCount()).To(Equal(0),
			"bastion kept running with port 22 open while reporting itself disabled")
		Expect(fakeCloud.PublicIPCount()).To(Equal(0))
	})

	It("tears the bastion down when disabled even if only its status was lost", func() {
		// Narrower than the case above: the condition survives and still reports
		// the bastion as available, only the bastion status fields are gone.
		// Gating the cleanup on hasBastionStatus left the server running here.
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.ServerCount()).To(Equal(1))

		By("losing the persisted bastion status while keeping the conditions")
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Status.Bastion = infrav1.StackitBastionStatus{}
		Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.ClusterBastionReadyCondition, metav1.ConditionTrue, "Available")

		By("disabling the bastion")
		got.Spec.Bastion.Enabled = false
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeCloud.ServerCount()).To(Equal(0),
			"bastion kept running because cleanup was gated on the bastion status")
		Expect(fakeCloud.PublicIPCount()).To(Equal(0))
	})

	It("cleans up the load balancer during deletion when it was disabled and its status was lost", func() {
		// Counterpart to the bastion case: flipping apiServerLoadBalancer.enabled
		// off neither deletes the load balancer nor clears its ID, so with the
		// status patch lost the deletion gate matched nothing and the load
		// balancer stayed behind.
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(1))

		By("losing the persisted load balancer ID")
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Status.APIServerLoadBalancerID = ""
		Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

		By("disabling the load balancer")
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.APIServerLoadBalancer.Enabled = false
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		By("deleting the cluster")
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeCloud.LoadBalancerCount()).To(Equal(0),
			"load balancer leaked because cleanup was gated on spec and status")
	})

	It("validates bastion specs", func() {
		spec := validBastionSpec()
		Expect(validateBastionSpec(spec)).To(Succeed())

		spec.AllowedCIDRs = []string{"not-a-cidr"}
		Expect(validateBastionSpec(spec)).To(MatchError(ContainSubstring("invalid CIDR")))
	})

	It("creates the bastion with cloud-init user data from a ConfigMap", func() {
		expectBastionCloudInit(
			ctx, reconciler, request, stackitKey, fakeCloud,
			"ConfigMap", "bastion-cloud-init-"+clusterName,
			"#cloud-config\npackages:\n- htop\n", createCloudInitConfigMap,
		)
	})

	It("creates the bastion with cloud-init user data from a Secret", func() {
		expectBastionCloudInit(
			ctx, reconciler, request, stackitKey, fakeCloud,
			"Secret", "bastion-cloud-init-secret-"+clusterName,
			"#cloud-config\npackages:\n- jq\n", createCloudInitSecret,
		)
	})

	It("marks the bastion not ready when cloud-init ref is missing", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		got.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{
			Kind: "ConfigMap",
			Name: "missing-" + clusterName,
			Key:  "userData",
		}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeFalse())
		Expect(fakeCloud.ServerCount()).To(Equal(0))
		expectCondition(got.Status.Conditions, infrav1.ClusterBastionReadyCondition, metav1.ConditionFalse, "CloudInitRefError")
	})

	It("recreates the bastion and node SSH access when referenced cloud-init changes", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		cloudInitName := "bastion-cloud-init-" + clusterName
		createCloudInitConfigMap(ctx, cloudInitName, namespace, "userData", "#cloud-config\npackages:\n- htop\n")
		got.Spec.Bastion = validBastionSpec()
		got.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{
			Kind: "ConfigMap",
			Name: cloudInitName,
			Key:  "userData",
		}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		oldServerID := got.Status.Bastion.ServerID
		Expect(oldServerID).NotTo(BeEmpty())
		_, err = fakeCloud.EnsureNodeSSHAccess(ctx, cloud.NodeSSHAccessInput{
			Name:                   got.Name + "-node-ssh",
			ServerID:               got.Status.Bastion.ServerID,
			BastionSecurityGroupID: got.Status.Bastion.SecurityGroupID,
			Tags:                   bastionservice.NodeSSHAccessTags(got),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(2))

		configMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cloudInitName, Namespace: namespace}, configMap)).To(Succeed())
		configMap.Data["userData"] = "#cloud-config\npackages:\n- jq\n"
		Expect(k8sClient.Update(ctx, configMap)).To(Succeed())

		result, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(fakeCloud.ServerCount()).To(Equal(0))
		Expect(fakeCloud.PublicIPCount()).To(Equal(0))
		Expect(fakeCloud.SecurityGroupCount()).To(Equal(0))

		result, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Bastion.ServerID).NotTo(BeEmpty())
		Expect(got.Status.Bastion.ServerID).NotTo(Equal(oldServerID))
		Expect(got.Status.Bastion.CloudInitHash).To(Equal(bastionCloudInitHash([]byte(configMap.Data["userData"]))))
		Expect(string(fakeCloud.ServerUserData(got.Status.Bastion.ServerID))).To(Equal(configMap.Data["userData"]))
	})

	It("marks the network not ready when the configured network does not exist", func() {
		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Network.ID = "99999999-9999-9999-9999-999999999999"
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.Ready).To(BeFalse())
		expectCondition(got.Status.Conditions, infrav1.ClusterNetworkReadyCondition, metav1.ConditionFalse, "NetworkNotFound")
		expectCondition(got.Status.Conditions, infrav1.ClusterReadyCondition, metav1.ConditionFalse, "NetworkNotFound")
	})

	It("requeues when network lookup returns a transient error", func() {
		fakeCloud.FailNextGetNetwork = fmt.Errorf("temporary network lookup failure: %w", cloud.ErrTransient)

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.ClusterNetworkReadyCondition, metav1.ConditionFalse, "NetworkNotFound")
		expectCondition(got.Status.Conditions, infrav1.ClusterReadyCondition, metav1.ConditionFalse, "NetworkNotFound")
	})

	It("marks credentials invalid without requeueing on unauthorized credentials", func() {
		reconciler.CloudClientFactory = func(context.Context, cloud.Credentials) (cloud.Client, error) {
			return nil, fmt.Errorf("authenticate: %w", cloud.ErrUnauthorized)
		}

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, infrav1.ClusterCredentialsReadyCondition, metav1.ConditionFalse, "CredentialsInvalid")
		expectCondition(got.Status.Conditions, infrav1.ClusterReadyCondition, metav1.ConditionFalse, "CredentialsInvalid")
	})

	It("does not call the cloud API when the owning Cluster is paused", func() {
		cluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster)).To(Succeed())
		cluster.Spec.Paused = new(true)
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
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(0))

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)
	})

	It("does not call the cloud API when the StackitCluster has the paused annotation", func() {
		got := &infrav1.StackitCluster{}
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
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(0))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		expectCondition(got.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)
	})

	It("deletes the provider-managed load balancer and removes the finalizer", func() {
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.APIServerLoadBalancerID).NotTo(BeEmpty())
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(1))

		Expect(k8sClient.Delete(ctx, got)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeCloud.LoadBalancerCount()).To(Equal(0))
		Eventually(func() bool {
			err := k8sClient.Get(ctx, stackitKey, &infrav1.StackitCluster{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("keeps the finalizer when load balancer deletion returns a transient error", func() {
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Status.APIServerLoadBalancerID).NotTo(BeEmpty())
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		fakeCloud.FailNextDeleteLB = fmt.Errorf("delete load balancer: %w", cloud.ErrTransient)

		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).To(HaveOccurred())
		Expect(fakeCloud.LoadBalancerCount()).To(Equal(1))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		Expect(got.Finalizers).To(ContainElement(infrav1.ClusterFinalizer))
	})

	It("maps owning Cluster events to StackitCluster reconcile requests", func() {
		cluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster)).To(Succeed())

		requests := reconciler.stackitClusterRequestsForCluster(ctx, cluster)
		Expect(requests).To(Equal([]reconcile.Request{request}))
	})

	It("maps bastion cloud-init ConfigMap and Secret events to StackitCluster reconcile requests", func() {
		configMapName := "bastion-cloud-init-" + clusterName
		secretName := "bastion-cloud-init-secret-" + clusterName
		createCloudInitConfigMap(ctx, configMapName, namespace, "userData", "#cloud-config\n")
		createCloudInitSecret(ctx, secretName, namespace, "userData", "#cloud-config\n")

		got := &infrav1.StackitCluster{}
		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion = validBastionSpec()
		got.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{
			Kind: "ConfigMap",
			Name: configMapName,
			Key:  "userData",
		}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		configMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, configMap)).To(Succeed())
		requests := reconciler.stackitClusterRequestsForCloudInitRef(ctx, configMap)
		Expect(requests).To(Equal([]reconcile.Request{request}))

		Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
		got.Spec.Bastion.CloudInitRef.Kind = "Secret"
		got.Spec.Bastion.CloudInitRef.Name = secretName
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)).To(Succeed())
		requests = reconciler.stackitClusterRequestsForCloudInitRef(ctx, secret)
		Expect(requests).To(Equal([]reconcile.Request{request}))
	})

	It("ignores Cluster events for other infrastructure providers", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: namespace},
			Spec: clusterv1.ClusterSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: "other.infrastructure.example.com",
					Kind:     "OtherCluster",
					Name:     "other",
				},
			},
		}

		requests := reconciler.stackitClusterRequestsForCluster(ctx, cluster)
		Expect(requests).To(BeEmpty())
	})
})

func expectBastionCloudInit(
	ctx context.Context,
	reconciler *StackitClusterReconciler,
	request reconcile.Request,
	stackitKey types.NamespacedName,
	fakeCloud *cloudfake.Client,
	kind, cloudInitName, cloudInit string,
	createCloudInit func(context.Context, string, string, string, string),
) {
	got := &infrav1.StackitCluster{}
	Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
	createCloudInit(ctx, cloudInitName, got.Namespace, "userData", cloudInit)
	got.Spec.Bastion = validBastionSpec()
	got.Spec.Bastion.CloudInitRef = &infrav1.StackitBastionCloudInitRef{
		Kind: kind,
		Name: cloudInitName,
		Key:  "userData",
	}
	Expect(k8sClient.Update(ctx, got)).To(Succeed())

	result, err := reconciler.Reconcile(ctx, request)
	Expect(err).NotTo(HaveOccurred())
	Expect(result).To(Equal(reconcile.Result{}))

	Expect(k8sClient.Get(ctx, stackitKey, got)).To(Succeed())
	Expect(got.Status.Bastion.ServerID).NotTo(BeEmpty())
	Expect(got.Status.Bastion.CloudInitHash).To(Equal(bastionCloudInitHash([]byte(cloudInit))))
	Expect(string(fakeCloud.ServerUserData(got.Status.Bastion.ServerID))).To(Equal(cloudInit))
}

func newStackitCluster(name, namespace string, lbEnabled bool) *infrav1.StackitCluster {
	return &infrav1.StackitCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       name,
				UID:        types.UID("cluster-" + name),
			}},
		},
		Spec: infrav1.StackitClusterSpec{
			ProjectID: testProjectID,
			Region:    "eu01",
			CredentialsSecretRef: corev1.SecretReference{
				Name:      "stackit-credentials",
				Namespace: namespace,
			},
			Network: infrav1.StackitClusterNetworkSpec{
				ID: testNetworkID,
			},
			APIServerLoadBalancer: infrav1.StackitAPIServerLoadBalancerSpec{
				Enabled: lbEnabled,
			},
			ControlPlaneEndpoint: clusterv1.APIEndpoint{
				Host: "198.51.100.1",
				Port: 6443,
			},
		},
	}
}

func validBastionSpec() infrav1.StackitBastionSpec {
	return infrav1.StackitBastionSpec{
		Enabled:      true,
		ImageID:      testImageID,
		MachineType:  "c2i.1",
		SSHKeyName:   "cluster-api-provider-stackit",
		AllowedCIDRs: []string{"203.0.113.10/32"},
	}
}
