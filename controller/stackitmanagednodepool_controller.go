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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
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
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

const (
	nodePoolDeletedReason = "Deleted"
)

// StackitManagedNodePoolReconciler reconciles a StackitManagedNodePool object.
//
// StackitManagedNodePoolReconciler is a pure Kubernetes lifecycle and finalizer
// controller. It makes zero SKE API calls and does not access STACKIT
// credentials; all SKE reads, writes, and child status projections are owned by
// StackitManagedClusterReconciler.
type StackitManagedNodePoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagednodepools/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmanagedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinepools,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch

func (r *StackitManagedNodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	log := logf.FromContext(ctx)

	nodePool := &infrav1.StackitManagedNodePool{}
	if err := r.Get(ctx, req.NamespacedName, nodePool); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !nodePool.DeletionTimestamp.IsZero() &&
		!controllerutil.ContainsFinalizer(nodePool, infrav1.ManagedNodePoolFinalizer) {
		return ctrl.Result{}, nil
	}

	machinePool, err := getMachinePoolForNodePool(ctx, r.Client, nodePool)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get owning MachinePool: %w", err)
	}

	cluster, err := getClusterForMachinePool(ctx, r.Client, machinePool)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get Cluster for MachinePool: %w", err)
	}
	if cluster != nil && (!isStackitManagedClusterRef(cluster.Spec.InfrastructureRef) || !isNodePoolAssociatedWithMachinePool(nodePool, machinePool, cluster)) {
		cluster = nil
	}

	var managedCluster *infrav1.StackitManagedCluster
	if cluster != nil {
		managedCluster, err = getStackitManagedClusterForCluster(ctx, r.Client, cluster)
		if err != nil {
			return ctrl.Result{}, err
		}
		if managedCluster != nil && hasIncompatibleNodePoolClusterUIDLabel(nodePool, managedCluster) {
			managedCluster = nil
		}
	}

	if nodePool.DeletionTimestamp.IsZero() {
		if machinePool == nil {
			log.Info("StackitManagedNodePool has no owning MachinePool yet, requeueing")
			return ctrl.Result{}, nil
		}
		if cluster == nil {
			log.Info("MachinePool has no owning Cluster referencing a StackitManagedCluster yet, requeueing", "machinePool", machinePool.Name)
			return ctrl.Result{}, nil
		}
		if managedCluster == nil {
			log.Info("StackitManagedCluster not found, requeueing", "managedCluster", cluster.Spec.InfrastructureRef.Name)
			return ctrl.Result{}, nil
		}
		if !cluster.DeletionTimestamp.IsZero() || !managedCluster.DeletionTimestamp.IsZero() {
			log.Info("Parent cluster is deleting, skipping finalizer addition for StackitManagedNodePool")
			return ctrl.Result{}, nil
		}
	}

	nodePoolScope, err := scope.NewManagedNodePoolScope(r.Client, cluster, machinePool, managedCluster, nodePool)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create managed node pool scope: %w", err)
	}
	defer func() {
		if patchErr := nodePoolScope.PatchObject(ctx); patchErr != nil {
			res = ctrl.Result{}
			err = kerrors.NewAggregate([]error{err, patchErr})
		}
	}()

	if !nodePool.DeletionTimestamp.IsZero() {
		r.reconcileDelete(ctx, nodePoolScope)
		return ctrl.Result{}, nil
	}

	if paused, message := managedNodePoolPaused(cluster, machinePool, managedCluster, nodePool); paused {
		util.SetPausedCondition(&nodePool.Status.Conditions, nodePool.Generation, true, message)
		return ctrl.Result{}, nil
	}
	util.SetPausedCondition(&nodePool.Status.Conditions, nodePool.Generation, false, "")

	if !controllerutil.ContainsFinalizer(nodePool, infrav1.ManagedNodePoolFinalizer) {
		controllerutil.AddFinalizer(nodePool, infrav1.ManagedNodePoolFinalizer)
	}
	if managedCluster.UID != "" {
		if nodePool.Labels == nil {
			nodePool.Labels = make(map[string]string)
		}
		nodePool.Labels[util.LabelClusterUID] = string(managedCluster.UID)
	}

	return ctrl.Result{}, nil
}

func (r *StackitManagedNodePoolReconciler) reconcileDelete(
	ctx context.Context,
	nodePoolScope *scope.ManagedNodePoolScope,
) {
	log := logf.FromContext(ctx)
	nodePool := nodePoolScope.StackitManagedNodePool
	cluster := nodePoolScope.Cluster
	machinePool := nodePoolScope.MachinePool
	managedCluster := nodePoolScope.StackitManagedCluster

	// Fast path: if any parent in the CAPI hierarchy is already gone or
	// deleting, release the node-pool finalizer immediately so parent cluster
	// teardown can delete the entire SKE cluster in one operation.
	if machinePool == nil ||
		cluster == nil ||
		managedCluster == nil ||
		!cluster.DeletionTimestamp.IsZero() ||
		!managedCluster.DeletionTimestamp.IsZero() {
		log.Info("Releasing StackitManagedNodePool finalizer because parent cluster is missing or deleting")
		controllerutil.RemoveFinalizer(nodePool, infrav1.ManagedNodePoolFinalizer)
		return
	}

	// Standalone deletion handshake: remove the finalizer once the parent
	// StackitManagedClusterReconciler confirms the external node pool is absent
	// from SKE by projecting Ready=False with Reason="Deleted".
	readyCond := meta.FindStatusCondition(nodePool.Status.Conditions, infrav1.ManagedNodePoolReadyCondition)
	if readyCond != nil &&
		readyCond.Status == metav1.ConditionFalse &&
		readyCond.Reason == nodePoolDeletedReason {
		log.Info("Confirmed external SKE node pool deletion, removing finalizer", "nodePoolName", nodePool.Spec.Name)
		controllerutil.RemoveFinalizer(nodePool, infrav1.ManagedNodePoolFinalizer)
		return
	}

	log.V(1).Info("Waiting for StackitManagedCluster to confirm SKE node pool removal", "nodePoolName", nodePool.Spec.Name)
}

func managedNodePoolPaused(
	cluster *clusterv1.Cluster,
	machinePool *clusterv1.MachinePool,
	managedCluster *infrav1.StackitManagedCluster,
	nodePool *infrav1.StackitManagedNodePool,
) (bool, string) {
	var reasons []string
	if cluster != nil && ptr.Deref(cluster.Spec.Paused, false) {
		reasons = append(reasons, "Cluster spec.paused is set to true")
	}
	if cluster != nil && annotations.HasPaused(cluster) {
		reasons = append(reasons, "Cluster has the cluster.x-k8s.io/paused annotation")
	}
	if managedCluster != nil && annotations.HasPaused(managedCluster) {
		reasons = append(reasons, "StackitManagedCluster has the cluster.x-k8s.io/paused annotation")
	}
	if machinePool != nil && annotations.HasPaused(machinePool) {
		reasons = append(reasons, "MachinePool has the cluster.x-k8s.io/paused annotation")
	}
	if annotations.HasPaused(nodePool) {
		reasons = append(reasons, "object has the cluster.x-k8s.io/paused annotation")
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, ", ")
}

// SetupWithManager registers the controller with the manager.
func (r *StackitManagedNodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.StackitManagedNodePool{}).
		Watches(
			&clusterv1.MachinePool{},
			handler.EnqueueRequestsFromMapFunc(r.stackitManagedNodePoolRequestsForMachinePool),
		).
		Watches(
			&infrav1.StackitManagedCluster{},
			handler.EnqueueRequestsFromMapFunc(r.stackitManagedNodePoolRequestsForManagedCluster),
			builder.WithPredicates(parentLifecycleWatchPredicate()),
		).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.stackitManagedNodePoolRequestsForCluster),
			builder.WithPredicates(parentLifecycleWatchPredicate()),
		).
		Named("stackitmanagednodepool").
		Complete(r)
}

func parentLifecycleWatchPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return parentLifecycleChanged(e)
		},
	}
}

func parentLifecycleChanged(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return false
	}
	if e.ObjectOld.GetDeletionTimestamp().IsZero() != e.ObjectNew.GetDeletionTimestamp().IsZero() {
		return true
	}
	if annotations.HasPaused(e.ObjectOld) != annotations.HasPaused(e.ObjectNew) {
		return true
	}
	oldCluster, okOld := e.ObjectOld.(*clusterv1.Cluster)
	newCluster, okNew := e.ObjectNew.(*clusterv1.Cluster)
	if okOld && okNew {
		return ptr.Deref(oldCluster.Spec.Paused, false) != ptr.Deref(newCluster.Spec.Paused, false)
	}
	return false
}

func (r *StackitManagedNodePoolReconciler) stackitManagedNodePoolRequestsForMachinePool(_ context.Context, obj client.Object) []reconcile.Request {
	machinePool, ok := obj.(*clusterv1.MachinePool)
	if !ok {
		return nil
	}
	return stackitManagedNodePoolRequestForMachinePool(machinePool)
}

func (r *StackitManagedNodePoolReconciler) stackitManagedNodePoolRequestsForManagedCluster(ctx context.Context, obj client.Object) []reconcile.Request {
	managedCluster, ok := obj.(*infrav1.StackitManagedCluster)
	if !ok || r.Client == nil {
		return nil
	}
	cluster, err := getVerifiedOwnerCluster(ctx, r.Client, managedCluster)
	if err != nil {
		return nil
	}
	entries, err := listManagedNodePoolsForCluster(ctx, r.Client, cluster, managedCluster)
	if err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(entries))
	for _, entry := range entries {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(entry.NodePool),
		})
	}
	return requests
}

func (r *StackitManagedNodePoolReconciler) stackitManagedNodePoolRequestsForCluster(ctx context.Context, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*clusterv1.Cluster)
	if !ok || r.Client == nil || !isStackitManagedClusterRef(cluster.Spec.InfrastructureRef) {
		return nil
	}
	managedCluster, err := getStackitManagedClusterForCluster(ctx, r.Client, cluster)
	if err != nil {
		return nil
	}
	entries, err := listManagedNodePoolsForCluster(ctx, r.Client, cluster, managedCluster)
	if err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(entries))
	for _, entry := range entries {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(entry.NodePool),
		})
	}
	return requests
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

func hasIncompatibleOwnerUID(refs []metav1.OwnerReference, kind string, expectedUID types.UID) bool {
	if expectedUID == "" {
		return false
	}
	for _, ref := range refs {
		if ref.Kind != kind {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil || gv.Group != clusterv1.GroupVersion.Group {
			continue
		}
		if ref.UID != "" && ref.UID != expectedUID {
			return true
		}
	}
	return false
}

func hasMatchingOwnerUID(refs []metav1.OwnerReference, kind string, expectedUID types.UID) bool {
	if expectedUID == "" {
		return false
	}
	for _, ref := range refs {
		if ref.Kind != kind {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil || gv.Group != clusterv1.GroupVersion.Group {
			continue
		}
		if ref.UID == expectedUID {
			return true
		}
	}
	return false
}

func hasIncompatibleNodePoolClusterUIDLabel(
	nodePool *infrav1.StackitManagedNodePool,
	managedCluster *infrav1.StackitManagedCluster,
) bool {
	if nodePool == nil || managedCluster == nil || managedCluster.UID == "" {
		return false
	}
	uidLabel := nodePool.Labels[util.LabelClusterUID]
	return uidLabel != "" && uidLabel != string(managedCluster.UID)
}

func getVerifiedOwnerCluster(
	ctx context.Context,
	c client.Client,
	managedCluster *infrav1.StackitManagedCluster,
) (*clusterv1.Cluster, error) {
	if managedCluster == nil {
		return nil, nil
	}
	cluster, err := clusterutil.GetOwnerCluster(ctx, c, managedCluster.ObjectMeta)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if cluster != nil && hasIncompatibleOwnerUID(managedCluster.OwnerReferences, "Cluster", cluster.UID) {
		return nil, nil
	}
	return cluster, nil
}

func isMachinePoolAssociatedWithCluster(machinePool *clusterv1.MachinePool, cluster *clusterv1.Cluster) bool {
	if machinePool == nil || cluster == nil || machinePool.Namespace != cluster.Namespace {
		return false
	}
	if hasIncompatibleOwnerUID(machinePool.OwnerReferences, "Cluster", cluster.UID) {
		return false
	}
	labelCluster := machinePool.Labels[clusterv1.ClusterNameLabel]
	if labelCluster != "" && labelCluster != cluster.Name {
		return false
	}
	if machinePool.Spec.ClusterName != "" && machinePool.Spec.ClusterName != cluster.Name {
		return false
	}
	return machinePool.Spec.ClusterName == cluster.Name || labelCluster == cluster.Name
}

func isNodePoolAssociatedWithMachinePool(
	nodePool *infrav1.StackitManagedNodePool,
	machinePool *clusterv1.MachinePool,
	cluster *clusterv1.Cluster,
) bool {
	if nodePool == nil || machinePool == nil || nodePool.Namespace != machinePool.Namespace {
		return false
	}
	if !isStackitManagedNodePoolRef(machinePool.Spec.Template.Spec.InfrastructureRef, nodePool.Name) {
		return false
	}
	if hasIncompatibleOwnerUID(nodePool.OwnerReferences, "MachinePool", machinePool.UID) {
		return false
	}
	if cluster != nil {
		if labelCluster := nodePool.Labels[clusterv1.ClusterNameLabel]; labelCluster != "" && labelCluster != cluster.Name {
			return false
		}
	}
	return true
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
		if !isNodePoolAssociatedWithMachinePool(nodePool, machinePool, nil) {
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
		if isNodePoolAssociatedWithMachinePool(nodePool, mp, nil) {
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

	var cluster *clusterv1.Cluster
	var err error
	if machinePool.Labels[clusterv1.ClusterNameLabel] != "" {
		cluster, err = clusterutil.GetClusterFromMetadata(ctx, c, machinePool.ObjectMeta)
		if err != nil && !errors.Is(err, clusterutil.ErrNoCluster) && !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	if cluster == nil && machinePool.Spec.ClusterName != "" {
		cluster, err = clusterutil.GetClusterByName(ctx, c, machinePool.Namespace, machinePool.Spec.ClusterName)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	if cluster != nil && !isMachinePoolAssociatedWithCluster(machinePool, cluster) {
		return nil, nil
	}
	return cluster, nil
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
	if hasIncompatibleOwnerUID(managedCluster.OwnerReferences, "Cluster", cluster.UID) {
		return nil, nil
	}
	return managedCluster, nil
}

// listManagedNodePoolsForCluster discovers all StackitManagedNodePools
// belonging to the given Cluster / StackitManagedCluster, sorted
// deterministically by Kubernetes resource name.
func listManagedNodePoolsForCluster(
	ctx context.Context,
	c client.Client,
	cluster *clusterv1.Cluster,
	managedCluster *infrav1.StackitManagedCluster,
) ([]scope.ManagedNodePoolEntry, error) {
	if managedCluster == nil {
		return nil, nil
	}

	targetCluster := cluster
	if targetCluster != nil {
		if !isStackitManagedClusterRef(targetCluster.Spec.InfrastructureRef) ||
			targetCluster.Spec.InfrastructureRef.Name != managedCluster.Name ||
			hasIncompatibleOwnerUID(managedCluster.OwnerReferences, "Cluster", targetCluster.UID) {
			return nil, nil
		}
	} else {
		clusterName, clusterUID := ownerClusterIdentity(managedCluster)
		if clusterName != "" {
			targetCluster = &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: managedCluster.Namespace,
					UID:       clusterUID,
				},
			}
		}
	}

	byName := make(map[string]scope.ManagedNodePoolEntry)
	if targetCluster != nil {
		machinePools := &clusterv1.MachinePoolList{}
		if err := c.List(ctx, machinePools, client.InNamespace(managedCluster.Namespace)); err != nil {
			return nil, fmt.Errorf("list MachinePools: %w", err)
		}

		for i := range machinePools.Items {
			mp := &machinePools.Items[i]
			if !isMachinePoolAssociatedWithCluster(mp, targetCluster) {
				continue
			}
			ref := mp.Spec.Template.Spec.InfrastructureRef
			if !isStackitManagedNodePoolRef(ref, "") {
				continue
			}
			nodePool := &infrav1.StackitManagedNodePool{}
			key := types.NamespacedName{Namespace: managedCluster.Namespace, Name: ref.Name}
			if err := c.Get(ctx, key, nodePool); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, fmt.Errorf("get StackitManagedNodePool %s: %w", key, err)
			}
			if !isNodePoolAssociatedWithMachinePool(nodePool, mp, targetCluster) {
				continue
			}
			if hasIncompatibleNodePoolClusterUIDLabel(nodePool, managedCluster) {
				continue
			}
			// When the owning Cluster object is gone (cluster == nil), require
			// positive UID proof so a terminating StackitManagedCluster cannot
			// match a newly created MachinePool/NodePool with the same cluster name.
			if cluster == nil {
				uidMatched := hasMatchingOwnerUID(mp.OwnerReferences, "Cluster", targetCluster.UID) ||
					(managedCluster.UID != "" && nodePool.Labels[util.LabelClusterUID] == string(managedCluster.UID))
				if !uidMatched {
					continue
				}
			}
			byName[nodePool.Name] = scope.ManagedNodePoolEntry{
				NodePool: nodePool,
				Paused:   annotations.HasPaused(mp) || annotations.HasPaused(nodePool),
			}
		}
	}

	// UID-safe deletion fallback: if a MachinePool was already deleted before
	// its child StackitManagedNodePool finished releasing its finalizer, match
	// only StackitManagedNodePools carrying the exact StackitManagedCluster UID
	// label stamped when the child finalizer was established.
	if (cluster == nil || !cluster.DeletionTimestamp.IsZero() || !managedCluster.DeletionTimestamp.IsZero()) &&
		managedCluster.UID != "" {
		uidTaggedPools := &infrav1.StackitManagedNodePoolList{}
		if err := c.List(ctx, uidTaggedPools,
			client.InNamespace(managedCluster.Namespace),
			client.MatchingLabels{util.LabelClusterUID: string(managedCluster.UID)},
		); err != nil {
			return nil, fmt.Errorf("list UID-labeled StackitManagedNodePools: %w", err)
		}
		for i := range uidTaggedPools.Items {
			np := &uidTaggedPools.Items[i]
			if np.Labels[util.LabelClusterUID] != string(managedCluster.UID) {
				continue
			}
			if _, exists := byName[np.Name]; !exists {
				byName[np.Name] = scope.ManagedNodePoolEntry{
					NodePool: np,
					Paused:   annotations.HasPaused(np),
				}
			}
		}
	}

	result := make([]scope.ManagedNodePoolEntry, 0, len(byName))
	for _, entry := range byName {
		result = append(result, entry)
	}
	slices.SortFunc(result, func(a, b scope.ManagedNodePoolEntry) int {
		return cmp.Compare(a.NodePool.Name, b.NodePool.Name)
	})
	return result, nil
}

func ownerClusterIdentity(managedCluster *infrav1.StackitManagedCluster) (string, types.UID) {
	for _, ref := range managedCluster.OwnerReferences {
		if ref.Kind != "Cluster" {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err == nil && gv.Group == clusterv1.GroupVersion.Group {
			return ref.Name, ref.UID
		}
	}
	return "", ""
}
