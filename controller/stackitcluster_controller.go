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
	"maps"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/record"
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
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/availabilityzone"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

// StackitClusterReconciler reconciles a StackitCluster object.
type StackitClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CloudClientFactory builds a cloud.Client from parsed credentials. It is
	// injected so tests can swap in the in-memory fake.
	CloudClientFactory cloud.Factory
}

// SetupWithManager registers the controller with the manager.
func (r *StackitClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.StackitCluster{}).
		Watches(&clusterv1.Cluster{}, handler.EnqueueRequestsFromMapFunc(r.filterForStackitClusterResources)).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.loadBalancerTargetsForControlPlanes),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: machineTargetPoolChanged}),
		).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForCloudInitRef)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForCloudInitRef)).
		Named("stackitcluster").
		Complete(r)
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *StackitClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, err error) {
	log := logf.FromContext(ctx)

	stackitCluster := &infrav1.StackitCluster{}
	if err := r.Get(ctx, req.NamespacedName, stackitCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cluster, err := clusterutil.GetOwnerCluster(ctx, r.Client, stackitCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get owner cluster: %w", err)
	}
	if cluster == nil {
		log.Info("StackitCluster has no owning Cluster yet, requeueing")
		return ctrl.Result{}, nil
	}

	clusterScope, err := scope.NewClusterScope(r.Client, cluster, stackitCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create cluster scope: %w", err)
	}
	defer func() {
		if patchErr := clusterScope.PatchObject(ctx); patchErr != nil && err == nil {
			err = patchErr
		}
	}()

	if paused, message := util.ReconciliationPaused(cluster, stackitCluster); paused {
		clusterScope.SetPausedCondition(true, message)
		return ctrl.Result{}, nil
	}
	clusterScope.SetPausedCondition(false, "")

	if !stackitCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterScope)
	}
	return r.reconcileNormal(ctx, clusterScope)
}

func (r *StackitClusterReconciler) filterForStackitClusterResources(_ context.Context, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*clusterv1.Cluster)
	if !ok {
		return nil
	}
	ref := cluster.Spec.InfrastructureRef
	if ref.APIGroup != infrav1.GroupVersion.Group || ref.Kind != "StackitCluster" || ref.Name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      ref.Name,
		},
	}}
}

// loadBalancerTargetsForControlPlanes keeps the API server load balancer target
// pool in step with the control plane. Worker machines never appear in it.
func (r *StackitClusterReconciler) loadBalancerTargetsForControlPlanes(ctx context.Context, obj client.Object) []reconcile.Request {
	machine, ok := obj.(*clusterv1.Machine)
	if !ok || !clusterutil.IsControlPlaneMachine(machine) {
		return nil
	}
	cluster, err := clusterutil.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	switch {
	// A missing label or an already deleted Cluster is routine during teardown.
	case errors.Is(err, clusterutil.ErrNoCluster) || apierrors.IsNotFound(err):
		return nil
	case err != nil:
		logf.FromContext(ctx).Error(err, "Failed to get Cluster for Machine watch", "machine", client.ObjectKeyFromObject(machine))
		return nil
	}
	return r.filterForStackitClusterResources(ctx, cluster)
}

// machineTargetPoolChanged drops the frequent control plane status updates that
// cannot change the target pool but would each cost a reconcile.
func machineTargetPoolChanged(e event.UpdateEvent) bool {
	oldMachine, okOld := e.ObjectOld.(*clusterv1.Machine)
	newMachine, okNew := e.ObjectNew.(*clusterv1.Machine)
	if !okOld || !okNew {
		return true
	}
	return !slices.Equal(oldMachine.Status.Addresses, newMachine.Status.Addresses) ||
		oldMachine.DeletionTimestamp.IsZero() != newMachine.DeletionTimestamp.IsZero() ||
		!maps.Equal(oldMachine.Labels, newMachine.Labels)
}

func (r *StackitClusterReconciler) stackitClusterRequestsForCloudInitRef(ctx context.Context, obj client.Object) []reconcile.Request {
	kind := ""
	switch obj.(type) {
	case *corev1.ConfigMap:
		kind = "ConfigMap"
	case *corev1.Secret:
		kind = "Secret"
	default:
		return nil
	}

	clusters := &infrav1.StackitClusterList{}
	if err := r.List(ctx, clusters, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list StackitClusters for bastion cloud-init watch", "object", client.ObjectKeyFromObject(obj))
		return nil
	}

	requests := make([]reconcile.Request, 0, len(clusters.Items))
	for _, cluster := range clusters.Items {
		ref := cluster.Spec.Bastion.CloudInitRef
		if ref == nil || ref.Kind != kind || ref.Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			},
		})
	}
	return requests
}

func (r *StackitClusterReconciler) reconcileNormal(ctx context.Context, clusterScope *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitCluster := clusterScope.StackitCluster

	if !controllerutil.ContainsFinalizer(stackitCluster, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(stackitCluster, infrav1.ClusterFinalizer)
		// Persisted immediately, before any cloud resource can be created, to
		// ensure nothing is running behind an object that carries no finalizer to
		// clean it up.
		if err := clusterScope.PatchObject(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("persist finalizer: %w", err)
		}
	}
	clusterScope.SetFailureDomains(scope.DefaultFailureDomains(stackitCluster.Spec.Region))

	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, stackitCluster)
	if err != nil {
		log.Error(err, "Failed to build cloud client for cluster", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "InvalidCredentials", "Failed to build cloud client: %v", err)
		return clusterScope.CredentialFailureResult(
			err,
			credentialsRetryRequeueAfter,
			infrav1.ClusterCredentialsReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	clusterScope.SetCloudClient(cloudClient)
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterCredentialsReadyCondition,
	)

	if err := availabilityzone.New(clusterScope).Reconcile(ctx); err != nil {
		return clusterScope.CloudFailureResult(
			"AvailabilityZonesError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.ClusterReadyCondition,
		)
	}

	if err := network.New(clusterScope).Reconcile(ctx); err != nil {
		if stackitCluster.Spec.Network.ID != "" {
			log.Error(err, "Failed to get network",
				"networkID", stackitCluster.Spec.Network.ID,
				"region", stackitCluster.Spec.Region,
			)
			record.Warnf(
				stackitCluster, "FailedGetNetwork",
				"Failed to get network %s in region %s: %v",
				stackitCluster.Spec.Network.ID, stackitCluster.Spec.Region, err,
			)
			return clusterScope.CloudFailureResult(
				"NetworkNotFound",
				err,
				retryableErrorRequeueAfter,
				false,
				infrav1.ClusterNetworkReadyCondition,
				infrav1.ClusterReadyCondition,
			)
		}
		log.Error(err, "Failed to reconcile managed network",
			"cluster", stackitCluster.Name,
			"region", stackitCluster.Spec.Region,
		)
		record.Warnf(
			stackitCluster, "FailedCreateNetwork",
			"Failed to reconcile managed network in region %s: %v",
			stackitCluster.Spec.Region, err,
		)
		return clusterScope.CloudFailureResult(
			"NetworkCreateError",
			err,
			retryableErrorRequeueAfter,
			false,
			infrav1.ClusterNetworkReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	if clusterScope.NetworkCreated() {
		record.Eventf(stackitCluster, "NetworkCreated", "Created managed network %s", stackitCluster.Status.Network.ID)
	}
	clusterScope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterNetworkReadyCondition,
	)

	if stackitCluster.Spec.APIServerLoadBalancer.Enabled {
		// STACKIT takes an explicit target list rather than label-based
		// membership, so exactly one reconciler owns the pool. Machines being
		// deleted drop out here, before the node is drained.
		machines, err := collections.GetFilteredMachinesForCluster(
			ctx,
			r.Client,
			clusterScope.Cluster,
			collections.ControlPlaneMachines(clusterScope.Cluster.Name),
			collections.ActiveMachines,
		)
		if err != nil {
			clusterScope.SetConditions(
				metav1.ConditionFalse,
				"MachineListError",
				err.Error(),
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			return ctrl.Result{}, fmt.Errorf("list control plane machines: %w", err)
		}
		targets := loadbalancer.APIServerTargets(machines.UnsortedList())

		// Seeding with the real targets lets a load balancer deleted out of band
		// come back with the control plane behind it.
		seed := targets
		if len(seed) == 0 {
			seed = []loadbalancer.Target{loadbalancer.BootstrapTarget(clusterScope.BootstrapTargetIP())}
		}
		clusterScope.SetAPIServerLoadBalancerTargets(targets, seed)

		previousEndpointHost := stackitCluster.Status.APIServerEndpoint.Host
		wasLBReady := meta.IsStatusConditionTrue(stackitCluster.Status.Conditions, infrav1.ClusterLoadBalancerReadyCondition)

		err = loadbalancer.New(clusterScope).Reconcile(ctx)
		if clusterScope.APIServerLoadBalancerCreated() {
			record.Eventf(
				stackitCluster, "LoadBalancerCreated",
				"Created API server load balancer %s", stackitCluster.Status.APIServerLoadBalancerID,
			)
		}
		if loadbalancer.IsTargetPoolUpdateError(err) {
			if !shared.IsRetryable(err) {
				record.Warnf(
					stackitCluster, "LoadBalancerTargetError",
					"Cannot update API server load balancer target pool: %v", err,
				)
			}
			// Scoped to the load balancer condition: failing the cluster would
			// stop the machine reconciler from replacing the broken machine.
			clusterScope.SetConditions(
				metav1.ConditionFalse,
				"LoadBalancerTargetError",
				err.Error(),
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			// Requeue even on a permanent error: this return skips SetReady, so
			// no machine exists yet whose events could retry it.
			return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, nil
		}
		if err != nil {
			log.Error(err, "Failed to create API server load balancer",
				"cluster", stackitCluster.Name,
				"region", stackitCluster.Spec.Region,
			)
			record.Warnf(
				stackitCluster, "FailedCreateLoadBalancer",
				"Failed to create API server load balancer: %v", err,
			)
			return clusterScope.CloudFailureResult(
				"LoadBalancerError",
				err,
				retryableErrorRequeueAfter,
				false,
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
		}
		if stackitCluster.Status.APIServerEndpoint.Host == "" {
			clusterScope.SetNotReady(
				"Provisioning",
				"waiting for API server load balancer IP address",
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		isLBReady := wasLBReady && previousEndpointHost == stackitCluster.Status.APIServerEndpoint.Host
		if !isLBReady {
			clusterScope.SetConditions(
				metav1.ConditionTrue,
				"Available",
				"",
				infrav1.ClusterLoadBalancerReadyCondition,
			)
			record.Eventf(
				stackitCluster, "LoadBalancerReady",
				"API server load balancer is ready at %s", stackitCluster.Status.APIServerEndpoint.Host,
			)
		}
	} else if stackitCluster.Spec.ControlPlaneEndpoint.Host != "" {
		stackitCluster.Status.APIServerEndpoint = stackitCluster.Spec.ControlPlaneEndpoint
		clusterScope.SetConditions(
			metav1.ConditionTrue,
			"Skipped",
			"external endpoint provided",
			infrav1.ClusterLoadBalancerReadyCondition,
		)
	} else {
		log.Error(errors.New("controlPlaneEndpoint host is empty"), "API server load balancer is disabled and controlPlaneEndpoint is empty", "cluster", stackitCluster.Name)
		record.Warn(stackitCluster, "EndpointMissing", "apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty")
		clusterScope.SetNotReady(
			"EndpointMissing",
			"apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty",
			infrav1.ClusterLoadBalancerReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	if result, ready, err := r.reconcileBastion(ctx, clusterScope); err != nil {
		log.Error(err, "Failed to reconcile bastion",
			"cluster", stackitCluster.Name,
		)
		record.Warnf(
			stackitCluster, "FailedCreateBastion",
			"Failed to reconcile bastion: %v", err,
		)
		return clusterScope.CloudFailureResult(
			"BastionError",
			err,
			retryableErrorRequeueAfter,
			false,
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	} else if !ready {
		return result, nil
	}

	clusterScope.SetReady()
	log.V(1).Info("Reconciled StackitCluster", "endpoint", stackitCluster.Status.APIServerEndpoint)
	return ctrl.Result{}, nil
}

func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, clusterScope *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	stackitCluster := clusterScope.StackitCluster

	// Machines resolve their credentials and project context through this
	// StackitCluster, so the finalizer has to stay while any of them remain.
	machines, err := collections.GetFilteredMachinesForCluster(ctx, r.Client, clusterScope.Cluster)
	if err != nil {
		log.Error(err, "Failed to list Machines for cluster during deletion", "cluster", clusterScope.Cluster.Name)
		record.Warnf(stackitCluster, "FailedListMachines",
			"Failed to list Machines for cluster %s: %v", clusterScope.Cluster.Name, err)
		return ctrl.Result{}, fmt.Errorf("list Machines for cluster %s: %w", clusterScope.Cluster.Name, err)
	}
	if machines.Len() > 0 {
		log.Info(
			"Waiting for Machines to be deleted before removing the StackitCluster finalizer",
			"remainingMachines", machines.Len(),
		)
		return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
	}

	// Cleanup runs unconditionally: neither spec nor status is a trustworthy
	// record of what exists in the cloud. The load balancer, public IP,
	// security group, server, and network services fall back to tag lookups and
	// tolerate NotFound.
	cloudClient, err := util.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, stackitCluster)
	if err != nil {
		// A missing credentials Secret cannot be recovered from and commonly
		// disappears first during namespace teardown, so finalize and make the
		// possible leak loud rather than stranding the cluster in Terminating.
		// Every other credentials problem is fixable and keeps retrying.
		if apierrors.IsNotFound(err) {
			log.Error(err, "Credentials secret is gone; finalizing without cloud cleanup", "cluster", stackitCluster.Name)
			record.Warnf(stackitCluster, "CleanupSkipped",
				"Credentials Secret is gone; finalizing without cloud cleanup. "+
					"Any remaining STACKIT resources for this cluster must be removed manually: %v", err)
			controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to build cloud client for cluster deletion", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "InvalidCredentials",
			"Failed to build cloud client for deletion: %v", err)
		return clusterScope.CredentialFailureResult(
			err,
			credentialsRetryRequeueAfter,
			infrav1.ClusterCredentialsReadyCondition,
		)
	}
	clusterScope.SetCloudClient(cloudClient)
	loadBalancerID := stackitCluster.Status.APIServerLoadBalancerID
	if err := loadbalancer.New(clusterScope).Delete(ctx); err != nil {
		log.Error(err, "Failed to delete API server load balancer", "cluster", stackitCluster.Name, "loadBalancerID", loadBalancerID)
		record.Warnf(stackitCluster, "FailedDeleteLoadBalancer",
			"Failed to delete API server load balancer %s: %v", loadBalancerID, err)
		return ctrl.Result{}, err
	}
	if loadBalancerID != "" {
		record.Eventf(
			stackitCluster, "LoadBalancerDeleted",
			"Deleted API server load balancer %s", loadBalancerID,
		)
	}
	if err := deleteBastion(ctx, clusterScope); err != nil {
		log.Error(err, "Failed to delete bastion", "cluster", stackitCluster.Name)
		record.Warnf(stackitCluster, "FailedDeleteBastion",
			"Failed to delete bastion: %v", err)
		return ctrl.Result{}, err
	}
	networkID := stackitCluster.Status.Network.ID
	if err := network.New(clusterScope).Delete(ctx); err != nil {
		if shared.IsConflict(err) {
			log.Info("Managed network still has resources attached; requeueing deletion",
				"cluster", stackitCluster.Name,
				"networkID", networkID,
			)
			clusterScope.SetConditions(
				metav1.ConditionFalse,
				"NetworkInUse",
				err.Error(),
				infrav1.ClusterNetworkReadyCondition,
			)
			record.Warnf(stackitCluster, "NetworkInUse",
				"Managed network %s still has active resources attached; retrying deletion: %v", networkID, err)
			return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
		}
		log.Error(err, "Failed to delete managed network",
			"cluster", stackitCluster.Name,
			"networkID", networkID,
		)
		record.Warnf(stackitCluster, "FailedDeleteNetwork",
			"Failed to delete managed network %s: %v", networkID, err)
		return ctrl.Result{}, err
	}
	if networkID != "" && stackitCluster.Spec.Network.ID == "" {
		record.Eventf(stackitCluster, "NetworkDeleted", "Deleted managed network %s", networkID)
	}
	controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}
