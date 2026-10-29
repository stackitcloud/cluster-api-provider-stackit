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
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

// StackitManagedNodePoolReconciler reconciles a StackitManagedNodePool object.
//
// Only the managed-cluster reconciliation path performs mutating SKE cluster
// API calls; this reconciler resolves CAPI ownership relationships and leaves
// SKE cluster payload mutation to StackitManagedClusterReconciler.
type StackitManagedNodePoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinepools,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch

func (r *StackitManagedNodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nodePool := &infrav1.StackitManagedNodePool{}
	if err := r.Get(ctx, req.NamespacedName, nodePool); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	machinePool, err := getMachinePoolForNodePool(ctx, r.Client, nodePool)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get owning MachinePool: %w", err)
	}
	if machinePool == nil {
		log.Info("StackitManagedNodePool has no owning MachinePool yet, requeueing")
		return ctrl.Result{}, nil
	}

	cluster, err := getClusterForMachinePool(ctx, r.Client, machinePool)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get Cluster for MachinePool: %w", err)
	}
	if cluster == nil {
		log.Info("MachinePool has no owning Cluster yet, requeueing", "machinePool", machinePool.Name)
		return ctrl.Result{}, nil
	}

	if !isStackitManagedClusterRef(cluster.Spec.InfrastructureRef) {
		log.Info("Cluster does not reference a StackitManagedCluster, skipping", "cluster", cluster.Name)
		return ctrl.Result{}, nil
	}

	managedCluster, err := getStackitManagedClusterForCluster(ctx, r.Client, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if managedCluster == nil {
		log.Info("StackitManagedCluster not found, requeueing", "managedCluster", cluster.Spec.InfrastructureRef.Name)
		return ctrl.Result{}, nil
	}

	if !nodePool.DeletionTimestamp.IsZero() {
		log.Info(
			"Reconciling deleting StackitManagedNodePool",
			"cluster", cluster.Name,
			"machinePool", machinePool.Name,
			"managedCluster", managedCluster.Name,
		)
		return ctrl.Result{}, nil
	}

	log.Info(
		"Reconciling StackitManagedNodePool",
		"cluster", cluster.Name,
		"machinePool", machinePool.Name,
		"managedCluster", managedCluster.Name,
	)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller with the manager.
func (r *StackitManagedNodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.StackitManagedNodePool{}).
		Watches(
			&clusterv1.MachinePool{},
			handler.EnqueueRequestsFromMapFunc(r.stackitManagedNodePoolRequestsForMachinePool),
		).
		Named("stackitmanagednodepool").
		Complete(r)
}

func (r *StackitManagedNodePoolReconciler) stackitManagedNodePoolRequestsForMachinePool(_ context.Context, obj client.Object) []reconcile.Request {
	machinePool, ok := obj.(*clusterv1.MachinePool)
	if !ok {
		return nil
	}
	return stackitManagedNodePoolRequestForMachinePool(machinePool)
}

func stackitManagedNodePoolRequestForMachinePool(machinePool *clusterv1.MachinePool) []reconcile.Request {
	if machinePool == nil {
		return nil
	}
	ref := machinePool.Spec.Template.Spec.InfrastructureRef
	if !isStackitManagedNodePoolRef(ref, "") {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: machinePool.Namespace,
			Name:      ref.Name,
		},
	}}
}

func isStackitManagedNodePoolRef(ref clusterv1.ContractVersionedObjectReference, expectedName string) bool {
	return ref.APIGroup == infrav1.GroupVersion.Group &&
		ref.Kind == "StackitManagedNodePool" &&
		ref.Name != "" &&
		(expectedName == "" || ref.Name == expectedName)
}

func isStackitManagedClusterRef(ref clusterv1.ContractVersionedObjectReference) bool {
	return ref.APIGroup == infrav1.GroupVersion.Group &&
		ref.Kind == "StackitManagedCluster" &&
		ref.Name != ""
}

func getMachinePoolForNodePool(
	ctx context.Context,
	c client.Client,
	nodePool *infrav1.StackitManagedNodePool,
) (*clusterv1.MachinePool, error) {
	if nodePool == nil {
		return nil, nil
	}

	machinePool, err := clusterutil.GetOwnerMachinePool(ctx, c, nodePool.ObjectMeta)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	if machinePool != nil {
		if !isStackitManagedNodePoolRef(machinePool.Spec.Template.Spec.InfrastructureRef, nodePool.Name) {
			return nil, nil
		}
		return machinePool, nil
	}

	machinePools := &clusterv1.MachinePoolList{}
	if err := c.List(ctx, machinePools, client.InNamespace(nodePool.Namespace)); err != nil {
		return nil, err
	}
	for i := range machinePools.Items {
		mp := &machinePools.Items[i]
		if isStackitManagedNodePoolRef(mp.Spec.Template.Spec.InfrastructureRef, nodePool.Name) {
			return mp, nil
		}
	}
	return nil, nil
}

func getClusterForMachinePool(
	ctx context.Context,
	c client.Client,
	machinePool *clusterv1.MachinePool,
) (*clusterv1.Cluster, error) {
	if machinePool == nil {
		return nil, nil
	}

	if machinePool.Labels[clusterv1.ClusterNameLabel] != "" {
		cluster, err := clusterutil.GetClusterFromMetadata(ctx, c, machinePool.ObjectMeta)
		switch {
		case errors.Is(err, clusterutil.ErrNoCluster) || apierrors.IsNotFound(err):
			return nil, nil
		case err != nil:
			return nil, err
		default:
			return cluster, nil
		}
	}

	if machinePool.Spec.ClusterName != "" {
		cluster, err := clusterutil.GetClusterByName(ctx, c, machinePool.Namespace, machinePool.Spec.ClusterName)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return cluster, err
	}

	return nil, nil
}

func getStackitManagedClusterForCluster(
	ctx context.Context,
	c client.Client,
	cluster *clusterv1.Cluster,
) (*infrav1.StackitManagedCluster, error) {
	if cluster == nil || !isStackitManagedClusterRef(cluster.Spec.InfrastructureRef) {
		return nil, nil
	}
	managedCluster := &infrav1.StackitManagedCluster{}
	key := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := c.Get(ctx, key, managedCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get StackitManagedCluster %s: %w", key, err)
	}
	return managedCluster, nil
}
