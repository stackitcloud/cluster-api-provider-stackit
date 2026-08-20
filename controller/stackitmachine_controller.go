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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

// StackitMachineReconciler reconciles a StackitMachine object.
type StackitMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CloudClientFactory builds a cloud.Client from parsed credentials.
	CloudClientFactory cloud.Factory
	Recorder           events.EventRecorder
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
func (r *StackitMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, err error) {
	log := logf.FromContext(ctx)

	stackitMachine := &infrav1.StackitMachine{}
	if err := r.Get(ctx, req.NamespacedName, stackitMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// The owning objects are resolved as far as they still exist. While the
	// object is being deleted an owner that is simply gone is tolerated, so the
	// checks that need one sit behind the deletion branch below rather than in
	// front of it. Every other error still fails, so a transient API problem
	// cannot be mistaken for a missing owner and drop the finalizer over a
	// server that is still running.
	deleting := !stackitMachine.DeletionTimestamp.IsZero()

	machine, ownerErr := clusterutil.GetOwnerMachine(ctx, r.Client, stackitMachine.ObjectMeta)
	if ownerErr != nil && (!deleting || !ownerGone(ownerErr)) {
		return ctrl.Result{}, fmt.Errorf("get owner machine: %w", ownerErr)
	}
	var cluster *clusterv1.Cluster
	if machine != nil {
		cluster, ownerErr = clusterutil.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
		if ownerErr != nil && (!deleting || !ownerGone(ownerErr)) {
			return ctrl.Result{}, fmt.Errorf("get cluster from machine metadata: %w", ownerErr)
		}
	}
	var stackitCluster *infrav1.StackitCluster
	if cluster != nil {
		stackitCluster, err = r.getStackitCluster(ctx, cluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	machineScope, err := scope.NewMachineScope(r.Client, cluster, machine, stackitCluster, stackitMachine)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create machine scope: %w", err)
	}
	defer func() {
		if patchErr := machineScope.PatchObject(ctx); patchErr != nil && err == nil {
			err = patchErr
		}
	}()

	if paused, message := util.ReconciliationPaused(cluster, stackitMachine); paused {
		util.SetPausedCondition(&stackitMachine.Status.Conditions, stackitMachine.Generation, true, message)
		return ctrl.Result{}, nil
	}
	util.SetPausedCondition(&stackitMachine.Status.Conditions, stackitMachine.Generation, false, "")

	if !stackitMachine.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDelete(ctx, machineScope)
	}

	switch {
	case machine == nil:
		log.Info("StackitMachine has no owning Machine yet, waiting")
		return ctrl.Result{}, nil
	case cluster == nil:
		log.Info("Machine has no owning Cluster yet, waiting")
		return ctrl.Result{}, nil
	case stackitCluster == nil:
		log.Info("StackitCluster not found, waiting")
		return ctrl.Result{}, nil
	}
	return r.reconcileNormal(ctx, machineScope)
}

// ownerGone reports whether err means the owning object no longer exists, as
// opposed to not being readable right now. GetOwnerMachine and GetOwnerCluster
// surface a plain NotFound; GetClusterFromMetadata reports ErrNoCluster when the
// Machine has lost the label naming its Cluster.
func ownerGone(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, clusterutil.ErrNoCluster)
}

// SetupWithManager registers the controller with the manager.
func (r *StackitMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.StackitMachine{}).
		Watches(&clusterv1.Machine{}, handler.EnqueueRequestsFromMapFunc(r.stackitMachineRequestsForMachine)).
		Watches(&infrav1.StackitCluster{}, handler.EnqueueRequestsFromMapFunc(r.stackitMachineRequestsForStackitCluster)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.stackitMachineRequestsForBootstrapSecret)).
		Named("stackitmachine").
		Complete(r)
}
