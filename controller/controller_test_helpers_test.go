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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

const (
	testProjectID = "11111111-1111-1111-1111-111111111111"
	testNetworkID = "22222222-2222-2222-2222-222222222222"
	testImageID   = "33333333-3333-3333-3333-333333333333"
)

func createCredentialsSecret(ctx context.Context, name, namespace, projectID string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string][]byte{
			"project-id":          []byte(projectID),
			"serviceaccount.json": []byte("{}"),
		},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
}

func createBootstrapSecret(ctx context.Context, name string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data: map[string][]byte{
			"value": []byte("bootstrap-data"),
		},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
}

func createCloudInitConfigMap(ctx context.Context, name, namespace, key, value string) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string]string{
			key: value,
		},
	}
	Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
}

func createCloudInitSecret(ctx context.Context, name, namespace, key, value string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string][]byte{
			key: []byte(value),
		},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
}

func createOwnerCluster(ctx context.Context, name string) {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "StackitCluster",
				Name:     name,
			},
		},
	}
	Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
}

func createOwnerMachine(ctx context.Context, name, clusterName, stackitMachineName string) {
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: clusterName,
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: clusterName,
			Bootstrap: clusterv1.Bootstrap{
				// Specs that need bootstrap data attach it with updateMachineBootstrapSecret.
				DataSecretName: new(""),
			},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "StackitMachine",
				Name:     stackitMachineName,
			},
		},
	}
	Expect(k8sClient.Create(ctx, machine)).To(Succeed())
}

func createReadyStackitCluster(ctx context.Context, name, namespace, credentialsName string) {
	stackitCluster := newStackitCluster(name, namespace, false)
	stackitCluster.Spec.CredentialsSecretRef.Name = credentialsName
	stackitCluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "203.0.113.10", Port: 6443}
	Expect(k8sClient.Create(ctx, stackitCluster)).To(Succeed())
	stackitCluster.Status.Ready = true
	stackitCluster.Status.APIServerEndpoint = clusterv1.APIEndpoint{Host: "203.0.113.10", Port: 6443}
	stackitCluster.Status.FailureDomains = stackitFailureDomains(stackitCluster.Spec.Region)
	Expect(k8sClient.Status().Update(ctx, stackitCluster)).To(Succeed())
}

func updateMachineBootstrapSecret(ctx context.Context, name, bootstrapSecretName string) {
	machine := &clusterv1.Machine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, machine)).To(Succeed())
	machine.Spec.Bootstrap.DataSecretName = &bootstrapSecretName
	Expect(k8sClient.Update(ctx, machine)).To(Succeed())
}

func updateMachineControlPlaneLabel(ctx context.Context, name string) {
	machine := &clusterv1.Machine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, machine)).To(Succeed())
	if machine.Labels == nil {
		machine.Labels = map[string]string{}
	}
	machine.Labels[clusterv1.MachineControlPlaneLabel] = ""
	Expect(k8sClient.Update(ctx, machine)).To(Succeed())
}

// createControlPlaneMachine creates a control plane Machine and registers its
// cleanup. An empty ip leaves it without addresses, as while provisioning.
func createControlPlaneMachine(ctx context.Context, name, clusterName, ip string) {
	createOwnerMachine(ctx, name, clusterName, "stackit-"+name)
	DeferCleanup(func() {
		deleteIfExists(ctx, &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
	})
	updateMachineControlPlaneLabel(ctx, name)
	if ip != "" {
		setMachineInternalIP(ctx, name, ip)
	}
}

// markMachineDeleting leaves a Machine with a deletion timestamp and a
// finalizer, the state a control plane node is in while it drains.
func markMachineDeleting(ctx context.Context, name string) {
	machine := &clusterv1.Machine{}
	key := client.ObjectKey{Name: name, Namespace: "default"}
	Expect(k8sClient.Get(ctx, key, machine)).To(Succeed())
	machine.Finalizers = append(machine.Finalizers, "test.stackit.cloud/block-deletion")
	Expect(k8sClient.Update(ctx, machine)).To(Succeed())
	Expect(k8sClient.Delete(ctx, machine)).To(Succeed())
}

// setMachineInternalIP stands in for the Cluster API machine controller, which
// copies status.addresses from the StackitMachine but does not run in envtest.
func setMachineInternalIP(ctx context.Context, name, ip string) {
	machine := &clusterv1.Machine{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, machine)).To(Succeed())
	machine.Status.Addresses = []clusterv1.MachineAddress{{
		Type:    clusterv1.MachineInternalIP,
		Address: ip,
	}}
	Expect(k8sClient.Status().Update(ctx, machine)).To(Succeed())
}

func expectCondition(conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			Expect(condition.Status).To(Equal(status))
			Expect(condition.Reason).To(Equal(reason))
			Expect(condition.ObservedGeneration).NotTo(BeZero())
			return
		}
	}
	Expect(false).To(BeTrue(), "condition not found: "+conditionType)
}

func deleteIfExists(ctx context.Context, obj client.Object) {
	key := client.ObjectKeyFromObject(obj)
	current := obj.DeepCopyObject().(client.Object)
	if err := k8sClient.Get(ctx, key, current); err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}
	if len(current.GetFinalizers()) > 0 {
		current.SetFinalizers(nil)
		Expect(k8sClient.Update(ctx, current)).To(Succeed())
	}
	err := k8sClient.Delete(ctx, current)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
}
