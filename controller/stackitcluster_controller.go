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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
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
	Recorder           record.EventRecorder
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
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
		util.SetPausedCondition(&stackitCluster.Status.Conditions, stackitCluster.Generation, true, message)
		return ctrl.Result{}, nil
	}
	util.SetPausedCondition(&stackitCluster.Status.Conditions, stackitCluster.Generation, false, "")

	if !stackitCluster.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDelete(ctx, clusterScope)
	}
	return r.reconcileNormal(ctx, clusterScope)
}

func (r *StackitClusterReconciler) stackitClusterRequestsForCluster(_ context.Context, obj client.Object) []reconcile.Request {
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

// SetupWithManager registers the controller with the manager.
func (r *StackitClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.StackitCluster{}).
		Watches(&clusterv1.Cluster{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForCluster)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForCloudInitRef)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.stackitClusterRequestsForCloudInitRef)).
		Named("stackitcluster").
		Complete(r)
}
