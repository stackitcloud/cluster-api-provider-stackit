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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

func (r *StackitMachineReconciler) stackitMachineRequestsForMachine(_ context.Context, obj client.Object) []reconcile.Request {
	machine, ok := obj.(*clusterv1.Machine)
	if !ok {
		return nil
	}
	return stackitMachineRequestForMachine(machine)
}

func (r *StackitMachineReconciler) stackitMachineRequestsForStackitCluster(ctx context.Context, obj client.Object) []reconcile.Request {
	stackitCluster, ok := obj.(*infrav1.StackitCluster)
	if !ok {
		return nil
	}

	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines, client.InNamespace(stackitCluster.Namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Machines for StackitCluster watch", "stackitCluster", client.ObjectKeyFromObject(stackitCluster))
		return nil
	}

	return stackitMachineRequestsForMachines(machines.Items, func(machine clusterv1.Machine) bool {
		return machine.Spec.ClusterName == stackitCluster.Name
	})
}

func (r *StackitMachineReconciler) stackitMachineRequestsForBootstrapSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines, client.InNamespace(secret.Namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Machines for bootstrap Secret watch", "secret", client.ObjectKeyFromObject(secret))
		return nil
	}

	return stackitMachineRequestsForMachines(machines.Items, func(machine clusterv1.Machine) bool {
		return machine.Spec.Bootstrap.DataSecretName != nil &&
			*machine.Spec.Bootstrap.DataSecretName == secret.Name
	})
}

func stackitMachineRequestsForMachines(machines []clusterv1.Machine, matches func(clusterv1.Machine) bool) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(machines))
	seen := map[types.NamespacedName]struct{}{}
	for _, machine := range machines {
		if !matches(machine) {
			continue
		}
		for _, request := range stackitMachineRequestForMachine(&machine) {
			if _, ok := seen[request.NamespacedName]; ok {
				continue
			}
			seen[request.NamespacedName] = struct{}{}
			requests = append(requests, request)
		}
	}
	return requests
}

func stackitMachineRequestForMachine(machine *clusterv1.Machine) []reconcile.Request {
	if machine == nil {
		return nil
	}
	ref := machine.Spec.InfrastructureRef
	if ref.APIGroup != infrav1.GroupVersion.Group || ref.Kind != "StackitMachine" || ref.Name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: machine.Namespace,
			Name:      ref.Name,
		},
	}}
}
