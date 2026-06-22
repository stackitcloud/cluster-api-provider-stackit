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
	"crypto/sha256"
	"fmt"
	"net/netip"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	ctrlhlp "github.com/stackitcloud/cluster-api-provider-stackit/internal/controller/helpers"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/scope"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

const (
	// defaultAPIServerPort is used when an LB is created without an explicit port.
	defaultAPIServerPort int32 = 6443

	cloudInitRefKindSecret = "Secret"

	retryableErrorRequeueAfter = 5 * time.Second
)

// StackitClusterReconciler reconciles a StackitCluster object.
type StackitClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CloudClientFactory builds a cloud.Client from parsed credentials. It is
	// injected so tests can swap in the in-memory fake.
	CloudClientFactory cloud.Factory
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Reconcile implements the spec section 18 flow.
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

	if paused, message := reconciliationPaused(cluster, stackitCluster); paused {
		setPausedCondition(&stackitCluster.Status.Conditions, stackitCluster.Generation, true, message)
		return ctrl.Result{}, nil
	}
	setPausedCondition(&stackitCluster.Status.Conditions, stackitCluster.Generation, false, "")

	if !stackitCluster.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDelete(ctx, clusterScope)
	}
	return r.reconcileNormal(ctx, clusterScope)
}

func (r *StackitClusterReconciler) reconcileNormal(ctx context.Context, s *scope.ClusterScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	sc := s.StackitCluster

	if !controllerutil.ContainsFinalizer(sc, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(sc, infrav1.ClusterFinalizer)
	}
	sc.Status.FailureDomains = stackitFailureDomains(sc.Spec.Region)

	cloudClient, err := ctrlhlp.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, sc)
	if err != nil {
		sc.Status.Ready = false
		return ctrlhlp.CredentialFailureResult(
			&sc.Status.Conditions,
			sc.Generation,
			err,
			infrav1.ClusterCredentialsReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	ctrlhlp.SetConditions(
		&sc.Status.Conditions,
		sc.Generation,
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterCredentialsReadyCondition,
	)

	network, err := cloudClient.GetNetwork(ctx, sc.Spec.Network.ID)
	if err != nil {
		sc.Status.Ready = false
		return ctrlhlp.CloudFailureResult(
			&sc.Status.Conditions,
			sc.Generation,
			"NetworkNotFound",
			err,
			retryableErrorRequeueAfter,
			false,
			infrav1.ClusterNetworkReadyCondition,
			infrav1.ClusterReadyCondition,
		)
	}
	ctrlhlp.SetConditions(
		&sc.Status.Conditions,
		sc.Generation,
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterNetworkReadyCondition,
	)

	if sc.Spec.APIServerLoadBalancer.Enabled {
		lb, err := cloudClient.EnsureAPIServerLoadBalancer(
			ctx,
			ctrlhlp.APIServerLoadBalancerInput(
				sc,
				[]cloud.LoadBalancerTargetInput{ctrlhlp.BootstrapAPIServerLoadBalancerTarget(bootstrapTargetIP(network))},
			),
		)
		if err != nil {
			sc.Status.Ready = false
			return ctrlhlp.CloudFailureResult(
				&sc.Status.Conditions,
				sc.Generation,
				"LoadBalancerError",
				err,
				retryableErrorRequeueAfter,
				false,
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
		}
		if lb != nil {
			sc.Status.APIServerLoadBalancerID = lb.ID
		}
		if lb == nil || lb.IP == "" {
			sc.Status.Ready = false
			ctrlhlp.SetConditions(
				&sc.Status.Conditions,
				sc.Generation,
				metav1.ConditionFalse,
				"Provisioning",
				"waiting for API server load balancer IP address",
				infrav1.ClusterLoadBalancerReadyCondition,
				infrav1.ClusterReadyCondition,
			)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		if lb == nil {
			return ctrl.Result{}, fmt.Errorf("%w: API server load balancer is nil", cloud.ErrTransient)
		}
		endpoint := clusterv1.APIEndpoint{
			Host: lb.IP,
			Port: defaultAPIServerPort,
		}
		sc.Spec.ControlPlaneEndpoint = endpoint
		sc.Status.APIServerEndpoint = endpoint
		ctrlhlp.SetConditions(
			&sc.Status.Conditions,
			sc.Generation,
			metav1.ConditionTrue,
			"Available",
			"",
			infrav1.ClusterLoadBalancerReadyCondition,
		)
	} else if sc.Spec.ControlPlaneEndpoint.Host != "" {
		sc.Status.APIServerEndpoint = sc.Spec.ControlPlaneEndpoint
		ctrlhlp.SetConditions(
			&sc.Status.Conditions,
			sc.Generation,
			metav1.ConditionTrue,
			"Skipped",
			"external endpoint provided",
			infrav1.ClusterLoadBalancerReadyCondition,
		)
	} else {
		sc.Status.Ready = false
		ctrlhlp.SetConditions(
			&sc.Status.Conditions,
			sc.Generation,
			metav1.ConditionFalse,
			"EndpointMissing",
			"apiServerLoadBalancer.enabled is false and controlPlaneEndpoint is empty",
			infrav1.ClusterLoadBalancerReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	if result, ready, err := r.reconcileBastion(ctx, cloudClient, sc); err != nil {
		sc.Status.Ready = false
		return ctrlhlp.CloudFailureResult(
			&sc.Status.Conditions,
			sc.Generation,
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

	sc.Status.Ready = true
	sc.Status.Initialization.Provisioned = true
	ctrlhlp.SetConditions(
		&sc.Status.Conditions,
		sc.Generation,
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterReadyCondition,
	)
	log.V(1).Info("StackitCluster ready", "endpoint", sc.Status.APIServerEndpoint)
	return ctrl.Result{}, nil
}

func stackitFailureDomains(region string) []clusterv1.FailureDomain {
	controlPlane := true
	return []clusterv1.FailureDomain{
		{
			Name:         region + "-1",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
		{
			Name:         region + "-2",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
		{
			Name:         region + "-3",
			ControlPlane: &controlPlane,
			Attributes: map[string]string{
				"region": region,
			},
		},
	}
}

func bootstrapTargetIP(network *cloud.Network) string {
	if network == nil {
		return "10.0.0.1"
	}
	for _, prefixValue := range network.IPv4Prefixes {
		prefix, err := netip.ParsePrefix(prefixValue)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		address := prefix.Masked().Addr()
		for range 10 {
			address = address.Next()
		}
		if prefix.Contains(address) {
			return address.String()
		}
	}
	return "10.0.0.1"
}

func (r *StackitClusterReconciler) reconcileBastion(
	ctx context.Context,
	cloudClient cloud.Client,
	sc *infrav1.StackitCluster,
) (ctrl.Result, bool, error) {
	input := ctrlhlp.BastionInput(sc, nil)
	status := cloud.Bastion{
		ServerID:        sc.Status.Bastion.ServerID,
		PublicIPID:      sc.Status.Bastion.PublicIPID,
		PublicIP:        sc.Status.Bastion.PublicIP,
		SecurityGroupID: sc.Status.Bastion.SecurityGroupID,
	}

	if !sc.Spec.Bastion.Enabled {
		if hasBastionStatus(sc.Status.Bastion) {
			if err := cloudClient.DeleteNodeSSHAccess(ctx, ctrlhlp.NodeSSHAccessTags(sc)); err != nil {
				return ctrl.Result{}, false, err
			}
			if err := cloudClient.DeleteBastion(ctx, input, status); err != nil {
				return ctrl.Result{}, false, err
			}
			sc.Status.Bastion = infrav1.StackitBastionStatus{}
		}
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
			metav1.ConditionTrue, "Skipped", "bastion disabled", sc.Generation)
		return ctrl.Result{}, true, nil
	}

	if err := validateBastionSpec(sc.Spec.Bastion); err != nil {
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
			metav1.ConditionFalse, "InvalidBastionSpec", err.Error(), sc.Generation)
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterReadyCondition,
			metav1.ConditionFalse, "InvalidBastionSpec", err.Error(), sc.Generation)
		sc.Status.Ready = false
		return ctrl.Result{}, false, nil
	}

	cloudInit, err := r.resolveBastionCloudInit(ctx, sc)
	if err != nil {
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
			metav1.ConditionFalse, "CloudInitRefError", err.Error(), sc.Generation)
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterReadyCondition,
			metav1.ConditionFalse, "CloudInitRefError", err.Error(), sc.Generation)
		sc.Status.Ready = false
		return ctrl.Result{}, false, nil
	}
	input.CloudInit = cloudInit

	if bastionNeedsRecreate(sc, cloudInit) {
		if err := cloudClient.DeleteNodeSSHAccess(ctx, ctrlhlp.NodeSSHAccessTags(sc)); err != nil && !cloud.IsNotFound(err) {
			return ctrl.Result{}, false, err
		}
		if err := cloudClient.DeleteBastion(ctx, input, status); err != nil && !cloud.IsNotFound(err) {
			return ctrl.Result{}, false, err
		}
		sc.Status.Bastion = infrav1.StackitBastionStatus{}
		sc.Status.Ready = false
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
			metav1.ConditionFalse, "Recreating", "recreating bastion because cloudInitRef content changed", sc.Generation)
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterReadyCondition,
			metav1.ConditionFalse, "Recreating", "recreating bastion because cloudInitRef content changed", sc.Generation)
		return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, false, nil
	}

	bastion, err := cloudClient.EnsureBastion(ctx, input)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	sc.Status.Bastion = infrav1.StackitBastionStatus{
		ServerID:        bastion.ServerID,
		PublicIPID:      bastion.PublicIPID,
		PublicIP:        bastion.PublicIP,
		SecurityGroupID: bastion.SecurityGroupID,
		CloudInitHash:   bastionCloudInitHash(cloudInit),
	}
	if bastion.ServerState != "" && bastion.ServerState != "ACTIVE" {
		sc.Status.Ready = false
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
			metav1.ConditionFalse, "Provisioning", fmt.Sprintf("bastion server state is %s", bastion.ServerState), sc.Generation)
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterReadyCondition,
			metav1.ConditionFalse, "Provisioning", fmt.Sprintf("bastion server state is %s", bastion.ServerState), sc.Generation)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}
	if bastion.PublicIP == "" {
		sc.Status.Ready = false
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
			metav1.ConditionFalse, "Provisioning", "waiting for bastion public IP address", sc.Generation)
		util.SetCondition(&sc.Status.Conditions, infrav1.ClusterReadyCondition,
			metav1.ConditionFalse, "Provisioning", "waiting for bastion public IP address", sc.Generation)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
	}
	util.SetCondition(&sc.Status.Conditions, infrav1.ClusterBastionReadyCondition,
		metav1.ConditionTrue, "Available", "", sc.Generation)
	return ctrl.Result{}, true, nil
}

func (r *StackitClusterReconciler) reconcileDelete(ctx context.Context, s *scope.ClusterScope) error {
	sc := s.StackitCluster
	if sc.Status.APIServerLoadBalancerID != "" || hasBastionStatus(sc.Status.Bastion) || sc.Spec.APIServerLoadBalancer.Enabled {
		cloudClient, err := ctrlhlp.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, sc)
		if err != nil {
			// If we cannot reach the cloud during delete, surface the condition
			// but do not block forever; finalizer removal is gated on the LB
			// deletion succeeding (or being already absent).
			ctrlhlp.SetConditions(
				&sc.Status.Conditions,
				sc.Generation,
				metav1.ConditionFalse,
				"CredentialsInvalid",
				err.Error(),
				infrav1.ClusterCredentialsReadyCondition,
			)
			return err
		}
		loadBalancerID, err := ctrlhlp.ResolveAPIServerLoadBalancerID(ctx, cloudClient, sc)
		if err != nil {
			return err
		}
		if loadBalancerID != "" {
			if err := cloudClient.DeleteAPIServerLoadBalancer(ctx, loadBalancerID); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			sc.Status.APIServerLoadBalancerID = ""
		}
		if hasBastionStatus(sc.Status.Bastion) {
			if err := cloudClient.DeleteNodeSSHAccess(ctx, ctrlhlp.NodeSSHAccessTags(sc)); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			if err := cloudClient.DeleteBastion(ctx, ctrlhlp.BastionInput(sc, nil), cloud.Bastion{
				ServerID:        sc.Status.Bastion.ServerID,
				PublicIPID:      sc.Status.Bastion.PublicIPID,
				PublicIP:        sc.Status.Bastion.PublicIP,
				SecurityGroupID: sc.Status.Bastion.SecurityGroupID,
			}); err != nil && !cloud.IsNotFound(err) {
				return err
			}
			sc.Status.Bastion = infrav1.StackitBastionStatus{}
		}
	}
	controllerutil.RemoveFinalizer(sc, infrav1.ClusterFinalizer)
	return nil
}

func validateBastionSpec(spec infrav1.StackitBastionSpec) error {
	if spec.ImageID == "" {
		return fmt.Errorf("%w: bastion.imageID is required", cloud.ErrInvalidInput)
	}
	if spec.MachineType == "" {
		return fmt.Errorf("%w: bastion.machineType is required", cloud.ErrInvalidInput)
	}
	if spec.SSHKeyName == "" {
		return fmt.Errorf("%w: bastion.sshKeyName is required", cloud.ErrInvalidInput)
	}
	if len(spec.AllowedCIDRs) == 0 {
		return fmt.Errorf("%w: bastion.allowedCIDRs is required", cloud.ErrInvalidInput)
	}
	for _, cidr := range spec.AllowedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("%w: bastion.allowedCIDRs contains invalid CIDR %q", cloud.ErrInvalidInput, cidr)
		}
	}
	return nil
}

func hasBastionStatus(status infrav1.StackitBastionStatus) bool {
	return status.ServerID != "" || status.PublicIPID != "" || status.PublicIP != "" || status.SecurityGroupID != ""
}

func bastionNeedsRecreate(sc *infrav1.StackitCluster, cloudInit []byte) bool {
	if !hasBastionStatus(sc.Status.Bastion) {
		return false
	}
	return sc.Status.Bastion.CloudInitHash != bastionCloudInitHash(cloudInit)
}

func bastionCloudInitHash(cloudInit []byte) string {
	if len(cloudInit) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(cloudInit))
}

func (r *StackitClusterReconciler) resolveBastionCloudInit(ctx context.Context, sc *infrav1.StackitCluster) ([]byte, error) {
	ref := sc.Spec.Bastion.CloudInitRef
	if ref == nil {
		return nil, nil
	}
	key := types.NamespacedName{Namespace: sc.Namespace, Name: ref.Name}
	switch ref.Kind {
	case "ConfigMap":
		configMap := &corev1.ConfigMap{}
		if err := r.Get(ctx, key, configMap); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("bastion.cloudInitRef ConfigMap %s not found", key)
			}
			return nil, err
		}
		value, ok := configMap.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("bastion.cloudInitRef key %q not found in ConfigMap %s", ref.Key, key)
		}
		return []byte(value), nil
	case cloudInitRefKindSecret:
		secret := &corev1.Secret{}
		if err := r.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("bastion.cloudInitRef Secret %s not found", key)
			}
			return nil, err
		}
		value, ok := secret.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("bastion.cloudInitRef key %q not found in Secret %s", ref.Key, key)
		}
		return append([]byte(nil), value...), nil
	default:
		return nil, fmt.Errorf("bastion.cloudInitRef.kind must be ConfigMap or Secret")
	}
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
