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
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/annotations"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	skeservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/ske"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

const (
	managedClusterDeleteRequeueAfter = 10 * time.Second
)

// StackitManagedClusterReconciler reconciles a StackitManagedCluster object.
type StackitManagedClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// SKEClientFactory builds an SKE API client from parsed credentials. It is
	// injected so tests can swap in an in-memory fake.
	SKEClientFactory cloud.SKEFactory
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagedclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagedclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagedclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinepools,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *StackitManagedClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	log := logf.FromContext(ctx)

	stackitManagedCluster := &infrav1.StackitManagedCluster{}
	if err := r.Get(ctx, req.NamespacedName, stackitManagedCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !stackitManagedCluster.DeletionTimestamp.IsZero() &&
		!hasManagedClusterFinalizer(stackitManagedCluster) {
		return ctrl.Result{}, nil
	}

	cluster, err := getVerifiedOwnerCluster(ctx, r.Client, stackitManagedCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get owner cluster: %w", err)
	}
	if cluster == nil && stackitManagedCluster.DeletionTimestamp.IsZero() {
		log.Info("StackitManagedCluster has no owning Cluster yet, requeueing")
		return ctrl.Result{}, nil
	}

	clusterScope, err := scope.NewManagedClusterScope(r.Client, cluster, stackitManagedCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create managed cluster scope: %w", err)
	}
	defer func() {
		if patchErr := clusterScope.PatchObject(ctx); patchErr != nil {
			res = ctrl.Result{}
			err = kerrors.NewAggregate([]error{err, patchErr})
		}
	}()

	if !stackitManagedCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterScope)
	}

	if paused, message := util.ReconciliationPaused(cluster, stackitManagedCluster); paused {
		util.SetPausedCondition(&stackitManagedCluster.Status.Conditions, stackitManagedCluster.Generation, true, message)
		return ctrl.Result{}, nil
	}
	util.SetPausedCondition(&stackitManagedCluster.Status.Conditions, stackitManagedCluster.Generation, false, "")

	return r.reconcileNormal(ctx, clusterScope)
}

func (r *StackitManagedClusterReconciler) reconcileNormal(
	ctx context.Context,
	clusterScope *scope.ManagedClusterScope,
) (ctrl.Result, error) {
	stackitManagedCluster := clusterScope.StackitManagedCluster

	if !controllerutil.ContainsFinalizer(stackitManagedCluster, infrav1.ManagedClusterFinalizer) {
		controllerutil.AddFinalizer(stackitManagedCluster, infrav1.ManagedClusterFinalizer)
		if err := clusterScope.PatchObject(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("persist finalizer: %w", err)
		}
	}

	nodePools, err := listManagedNodePoolsForCluster(ctx, r.Client, clusterScope.Cluster, stackitManagedCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list StackitManagedNodePools: %w", err)
	}
	if err := clusterScope.SetManagedNodePools(nodePools); err != nil {
		return ctrl.Result{}, fmt.Errorf("snapshot StackitManagedNodePool patch helpers: %w", err)
	}

	skeClient, err := util.BuildSKEClient(ctx, r.Client, r.SKEClientFactory, stackitManagedCluster)
	if err != nil {
		clusterScope.SetNotReady("CredentialsError", err.Error())
		return ctrl.Result{}, err
	}
	clusterScope.SetSKEClient(skeClient)

	return skeservice.New(clusterScope).Reconcile(ctx)
}

func (r *StackitManagedClusterReconciler) reconcileDelete(
	ctx context.Context,
	clusterScope *scope.ManagedClusterScope,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitManagedCluster := clusterScope.StackitManagedCluster

	nodePools, err := listManagedNodePoolsForCluster(ctx, r.Client, clusterScope.Cluster, stackitManagedCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list StackitManagedNodePools for deletion: %w", err)
	}
	if len(nodePools) > 0 {
		for _, entry := range nodePools {
			np := entry.NodePool
			if np.DeletionTimestamp.IsZero() {
				if deleteErr := r.Delete(ctx, np); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
					return ctrl.Result{}, fmt.Errorf("delete child StackitManagedNodePool %s: %w", np.Name, deleteErr)
				}
			}
		}
		log.Info("Waiting for StackitManagedNodePools to be deleted before deleting SKE cluster", "remainingNodePools", len(nodePools))
		clusterScope.SetNotReady(
			"WaitingForNodePoolsDeletion",
			fmt.Sprintf("waiting for %d StackitManagedNodePool(s) to be deleted", len(nodePools)),
		)
		return ctrl.Result{RequeueAfter: managedClusterDeleteRequeueAfter}, nil
	}

	skeClient, err := util.BuildSKEClient(ctx, r.Client, r.SKEClientFactory, stackitManagedCluster)
	if err != nil {
		clusterScope.SetNotReady("CredentialsError", err.Error())
		return ctrl.Result{}, err
	}
	clusterScope.SetSKEClient(skeClient)

	res, err := skeservice.New(clusterScope).Delete(ctx)
	if err != nil || res.RequeueAfter > 0 {
		return res, err
	}

	removeManagedClusterFinalizers(stackitManagedCluster)
	return ctrl.Result{}, nil
}

func hasManagedClusterFinalizer(managedCluster *infrav1.StackitManagedCluster) bool {
	return controllerutil.ContainsFinalizer(managedCluster, infrav1.ManagedClusterFinalizer) ||
		controllerutil.ContainsFinalizer(managedCluster, infrav1.LegacyManagedClusterFinalizer)
}

func removeManagedClusterFinalizers(managedCluster *infrav1.StackitManagedCluster) {
	controllerutil.RemoveFinalizer(managedCluster, infrav1.ManagedClusterFinalizer)
	controllerutil.RemoveFinalizer(managedCluster, infrav1.LegacyManagedClusterFinalizer)
}

// SetupWithManager registers the controller with the manager.
func (r *StackitManagedClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.StackitManagedCluster{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.filterForStackitManagedClusterResources),
		).
		Watches(
			&clusterv1.MachinePool{},
			handler.EnqueueRequestsFromMapFunc(r.stackitManagedClusterRequestsForMachinePool),
			builder.WithPredicates(machinePoolWatchPredicate()),
		).
		Watches(
			&infrav1.StackitManagedNodePool{},
			handler.EnqueueRequestsFromMapFunc(r.stackitManagedClusterRequestsForNodePool),
			builder.WithPredicates(managedNodePoolWatchPredicate()),
		).
		Named("stackitmanagedcluster").
		Complete(r)
}

func managedNodePoolWatchPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return managedNodePoolChanged(e)
		},
	}
}

// managedNodePoolChanged filters out status-only updates written by
// StackitManagedClusterReconciler while allowing desired-state, deletion,
// finalizer, ownership, label, and pause transitions to enqueue the parent
// StackitManagedCluster.
func managedNodePoolChanged(e event.UpdateEvent) bool {
	oldPool, okOld := e.ObjectOld.(*infrav1.StackitManagedNodePool)
	newPool, okNew := e.ObjectNew.(*infrav1.StackitManagedNodePool)
	if !okOld || !okNew {
		return true
	}
	return oldPool.GetGeneration() != newPool.GetGeneration() ||
		oldPool.GetDeletionTimestamp().IsZero() != newPool.GetDeletionTimestamp().IsZero() ||
		controllerutil.ContainsFinalizer(oldPool, infrav1.ManagedNodePoolFinalizer) !=
			controllerutil.ContainsFinalizer(newPool, infrav1.ManagedNodePoolFinalizer) ||
		!slices.Equal(oldPool.GetOwnerReferences(), newPool.GetOwnerReferences()) ||
		oldPool.GetLabels()[clusterv1.ClusterNameLabel] != newPool.GetLabels()[clusterv1.ClusterNameLabel] ||
		oldPool.GetLabels()[util.LabelClusterUID] != newPool.GetLabels()[util.LabelClusterUID] ||
		annotations.HasPaused(oldPool) != annotations.HasPaused(newPool)
}

func machinePoolWatchPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return machinePoolChanged(e)
		},
	}
}

func machinePoolChanged(e event.UpdateEvent) bool {
	oldMP, okOld := e.ObjectOld.(*clusterv1.MachinePool)
	newMP, okNew := e.ObjectNew.(*clusterv1.MachinePool)
	if !okOld || !okNew {
		return true
	}
	return oldMP.GetDeletionTimestamp().IsZero() != newMP.GetDeletionTimestamp().IsZero() ||
		annotations.HasPaused(oldMP) != annotations.HasPaused(newMP) ||
		oldMP.Spec.ClusterName != newMP.Spec.ClusterName ||
		oldMP.Spec.Template.Spec.InfrastructureRef != newMP.Spec.Template.Spec.InfrastructureRef ||
		!slices.Equal(oldMP.GetOwnerReferences(), newMP.GetOwnerReferences()) ||
		oldMP.GetLabels()[clusterv1.ClusterNameLabel] != newMP.GetLabels()[clusterv1.ClusterNameLabel]
}

func (r *StackitManagedClusterReconciler) filterForStackitManagedClusterResources(_ context.Context, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*clusterv1.Cluster)
	if !ok {
		return nil
	}
	ref := cluster.Spec.InfrastructureRef
	if !isStackitManagedClusterRef(ref) {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      ref.Name,
		},
	}}
}

func (r *StackitManagedClusterReconciler) stackitManagedClusterRequestsForMachinePool(ctx context.Context, obj client.Object) []reconcile.Request {
	machinePool, ok := obj.(*clusterv1.MachinePool)
	if !ok || r.Client == nil || !isStackitManagedNodePoolRef(machinePool.Spec.Template.Spec.InfrastructureRef, "") {
		return nil
	}
	cluster, err := getClusterForMachinePool(ctx, r.Client, machinePool)
	if err != nil || cluster == nil {
		return nil
	}
	return r.filterForStackitManagedClusterResources(ctx, cluster)
}

func (r *StackitManagedClusterReconciler) stackitManagedClusterRequestsForNodePool(ctx context.Context, obj client.Object) []reconcile.Request {
	nodePool, ok := obj.(*infrav1.StackitManagedNodePool)
	if !ok || r.Client == nil {
		return nil
	}

	machinePool, err := getMachinePoolForNodePool(ctx, r.Client, nodePool)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to get MachinePool for StackitManagedNodePool watch", "nodePool", client.ObjectKeyFromObject(nodePool))
		return nil
	}
	if machinePool != nil {
		cluster, err := getClusterForMachinePool(ctx, r.Client, machinePool)
		if err != nil {
			logf.FromContext(ctx).Error(err, "Failed to get Cluster for StackitManagedNodePool watch", "machinePool", client.ObjectKeyFromObject(machinePool))
			return nil
		}
		if cluster != nil {
			managedCluster, err := getStackitManagedClusterForCluster(ctx, r.Client, cluster)
			if err != nil || (managedCluster != nil && hasIncompatibleNodePoolClusterUIDLabel(nodePool, managedCluster)) {
				return nil
			}
			return r.filterForStackitManagedClusterResources(ctx, cluster)
		}
	}

	// UID-safe fallback during teardown when MachinePool or Cluster was already
	// deleted: match the owning StackitManagedCluster by its exact UID label.
	uidLabel := nodePool.Labels[util.LabelClusterUID]
	if uidLabel == "" {
		return nil
	}
	managedClusters := &infrav1.StackitManagedClusterList{}
	if err := r.List(ctx, managedClusters, client.InNamespace(nodePool.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range managedClusters.Items {
		mc := &managedClusters.Items[i]
		if string(mc.UID) == uidLabel {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(mc),
			})
		}
	}
	return requests
}
