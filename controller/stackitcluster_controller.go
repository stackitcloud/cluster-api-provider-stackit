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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/bastion"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
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

	reconcilers := []cloud.Reconciler{
		availabilityzone.New(clusterScope),
		network.New(clusterScope),
		loadbalancer.New(clusterScope),
		bastion.New(clusterScope),
	}
	for _, s := range reconcilers {
		res, err := s.Reconcile(ctx)
		if err != nil || !res.IsZero() || clusterScope.NotReady() {
			return res, err
		}
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

	reconcilers := []cloud.Reconciler{
		loadbalancer.New(clusterScope),
		bastion.New(clusterScope),
		network.New(clusterScope),
	}
	for _, s := range reconcilers {
		res, err := s.Delete(ctx)
		if err != nil || !res.IsZero() {
			return res, err
		}
	}

	controllerutil.RemoveFinalizer(stackitCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}
