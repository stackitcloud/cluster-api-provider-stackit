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

// StackitMachineReconciler reconciles a StackitMachine object.
type StackitMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CloudClientFactory builds a cloud.Client from parsed credentials.
	CloudClientFactory cloud.Factory
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitmachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=stackitclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile implements the spec section 19 flow.
func (r *StackitMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, err error) {
	log := logf.FromContext(ctx)

	stackitMachine := &infrav1.StackitMachine{}
	if err := r.Get(ctx, req.NamespacedName, stackitMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	machine, err := clusterutil.GetOwnerMachine(ctx, r.Client, stackitMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get owner machine: %w", err)
	}
	if machine == nil {
		log.Info("StackitMachine has no owning Machine yet, requeueing")
		return ctrl.Result{}, nil
	}

	cluster, err := clusterutil.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get cluster from machine metadata: %w", err)
	}
	if cluster == nil {
		log.Info("Machine has no owning Cluster yet, requeueing")
		return ctrl.Result{}, nil
	}

	stackitCluster, err := r.getStackitCluster(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if stackitCluster == nil {
		log.Info("StackitCluster not found, requeueing")
		return ctrl.Result{}, nil
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

	if paused, message := reconciliationPaused(cluster, stackitMachine); paused {
		setPausedCondition(&stackitMachine.Status.Conditions, stackitMachine.Generation, true, message)
		return ctrl.Result{}, nil
	}
	setPausedCondition(&stackitMachine.Status.Conditions, stackitMachine.Generation, false, "")

	if !stackitMachine.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDelete(ctx, machineScope)
	}
	return r.reconcileNormal(ctx, machineScope)
}

func (r *StackitMachineReconciler) reconcileNormal(ctx context.Context, s *scope.MachineScope) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	sm := s.StackitMachine

	if !controllerutil.ContainsFinalizer(sm, infrav1.MachineFinalizer) {
		controllerutil.AddFinalizer(sm, infrav1.MachineFinalizer)
	}

	if !s.StackitCluster.Status.Ready {
		ctrlhlp.SetConditions(
			&sm.Status.Conditions,
			sm.Generation,
			metav1.ConditionFalse,
			"InfrastructureNotReady",
			"waiting for StackitCluster to be ready",
			infrav1.MachineReadyCondition,
		)
		return ctrl.Result{}, nil
	}
	if err := validateMachineAvailabilityZone(s); err != nil {
		ctrlhlp.SetConditions(
			&sm.Status.Conditions,
			sm.Generation,
			metav1.ConditionFalse,
			"InvalidFailureDomain",
			err.Error(),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	bootstrapData, condStatus, reason, msg := r.fetchBootstrapData(ctx, s.Machine)
	ctrlhlp.SetConditions(&sm.Status.Conditions, sm.Generation, condStatus, reason, msg, infrav1.MachineBootstrapReadyCondition)
	if condStatus != metav1.ConditionTrue {
		ctrlhlp.SetConditions(&sm.Status.Conditions, sm.Generation, metav1.ConditionFalse, reason, msg, infrav1.MachineReadyCondition)
		// Per spec 13.3, missing/not-found cases requeue; invalid does not.
		if reason == util.BootstrapReasonInvalid {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: retryableErrorRequeueAfter}, nil
	}

	cloudClient, err := ctrlhlp.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, s.StackitCluster)
	if err != nil {
		return ctrlhlp.CredentialFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			err,
			infrav1.MachineCredentialsReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	ctrlhlp.SetConditions(
		&sm.Status.Conditions,
		sm.Generation,
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.MachineCredentialsReadyCondition,
	)

	tags := util.MachineTags(
		s.Cluster.Name,
		s.Cluster.Namespace,
		s.Machine.Name,
		string(s.Machine.UID),
		s.StackitMachine.Spec.AdditionalLabels,
	)

	server, err := r.ensureServer(ctx, cloudClient, s, bootstrapData, tags)
	if err != nil {
		return ctrlhlp.CloudFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			"InstanceError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}

	sm.Status.InstanceID = server.ID
	sm.Status.InstanceState = server.State
	sm.Status.Addresses = machineAddressesFromCloud(server.Addresses)

	providerID := cloud.NewProviderID(s.StackitCluster.Spec.ProjectID, s.StackitCluster.Spec.Region, server.ID)
	sm.Spec.ProviderID = &providerID
	sm.Status.ProviderID = providerID
	sm.Status.Initialization.Provisioned = true

	if server.State != "" && server.State != "ACTIVE" {
		sm.Status.Ready = false
		ctrlhlp.SetConditions(
			&sm.Status.Conditions,
			sm.Generation,
			metav1.ConditionFalse,
			"Provisioning",
			fmt.Sprintf("server state is %s", server.State),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if err := r.reconcileBastionNodeSSHAccess(ctx, cloudClient, s, server); err != nil {
		return ctrlhlp.CloudFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			"BastionSSHAccessError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	if err := r.reconcileAPIServerLoadBalancerTarget(ctx, cloudClient, s, server); err != nil {
		return ctrlhlp.CloudFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			"LoadBalancerTargetError",
			err,
			retryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}

	sm.Status.Ready = true

	ctrlhlp.SetConditions(
		&sm.Status.Conditions,
		sm.Generation,
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.MachineInstanceReadyCondition,
		infrav1.MachineReadyCondition,
	)
	log.V(1).Info("StackitMachine ready", "providerID", providerID)
	return ctrl.Result{}, nil
}

func validateMachineAvailabilityZone(s *scope.MachineScope) error {
	availabilityZone := s.StackitMachine.Spec.AvailabilityZone
	if availabilityZone == "" || len(s.StackitCluster.Status.FailureDomains) == 0 {
		return nil
	}
	for _, failureDomain := range s.StackitCluster.Status.FailureDomains {
		if failureDomain.Name == availabilityZone {
			return nil
		}
	}
	return fmt.Errorf("availabilityZone %q is not published in StackitCluster status.failureDomains", availabilityZone)
}

func (r *StackitMachineReconciler) reconcileDelete(ctx context.Context, s *scope.MachineScope) error {
	sm := s.StackitMachine
	needsLoadBalancerCleanup := isControlPlaneMachine(s.Machine) &&
		s.StackitCluster.Spec.APIServerLoadBalancer.Enabled &&
		s.StackitCluster.Status.APIServerLoadBalancerID != ""
	if sm.Status.InstanceID == "" && !needsLoadBalancerCleanup {
		controllerutil.RemoveFinalizer(sm, infrav1.MachineFinalizer)
		return nil
	}
	cloudClient, err := ctrlhlp.BuildCloudClient(ctx, r.Client, r.CloudClientFactory, s.StackitCluster)
	if err != nil {
		_, resultErr := ctrlhlp.CredentialFailureResult(
			&sm.Status.Conditions,
			sm.Generation,
			err,
			infrav1.MachineCredentialsReadyCondition,
		)
		return resultErr
	}
	if err := r.deleteAPIServerLoadBalancerTarget(ctx, cloudClient, s); err != nil {
		return err
	}
	if sm.Status.InstanceID == "" {
		controllerutil.RemoveFinalizer(sm, infrav1.MachineFinalizer)
		return nil
	}
	if err := cloudClient.DeleteServer(ctx, sm.Status.InstanceID); err != nil && !cloud.IsNotFound(err) {
		return err
	}
	sm.Status.InstanceID = ""
	sm.Status.InstanceState = ""
	controllerutil.RemoveFinalizer(sm, infrav1.MachineFinalizer)
	return nil
}

func (r *StackitMachineReconciler) fetchBootstrapData(ctx context.Context, machine *clusterv1.Machine) ([]byte, metav1.ConditionStatus, string, string) {
	if machine.Spec.Bootstrap.DataSecretName == nil || *machine.Spec.Bootstrap.DataSecretName == "" {
		return nil, metav1.ConditionFalse, "BootstrapDataSecretMissing", "Machine.spec.bootstrap.dataSecretName is empty"
	}
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, metav1.ConditionFalse, "BootstrapDataSecretNotFound", fmt.Sprintf("bootstrap secret %s not found", key)
		}
		return nil, metav1.ConditionFalse, "BootstrapDataSecretError", err.Error()
	}
	data, err := util.ExtractBootstrapData(secret)
	if err != nil {
		return nil, metav1.ConditionFalse, util.BootstrapReasonInvalid, err.Error()
	}
	return data, metav1.ConditionTrue, "Available", ""
}

func (r *StackitMachineReconciler) ensureServer(
	ctx context.Context,
	c cloud.Client,
	s *scope.MachineScope,
	userData []byte,
	tags map[string]string,
) (*cloud.Server, error) {
	sm := s.StackitMachine
	if sm.Status.InstanceID != "" {
		server, err := c.GetServer(ctx, sm.Status.InstanceID)
		if err == nil {
			return server, nil
		}
		if !cloud.IsNotFound(err) {
			return nil, err
		}
		// fall through to lookup-by-tags / re-create.
	}
	if server, err := c.FindServerByTags(ctx, tags); err == nil {
		return server, nil
	} else if !cloud.IsNotFound(err) {
		return nil, err
	}
	deleteOnTermination := true
	if sm.Spec.RootVolume.DeleteOnTermination != nil {
		deleteOnTermination = *sm.Spec.RootVolume.DeleteOnTermination
	}
	return c.CreateServer(ctx, cloud.CreateServerInput{
		Name:             sm.Name,
		ProjectID:        s.StackitCluster.Spec.ProjectID,
		Region:           s.StackitCluster.Spec.Region,
		ImageID:          sm.Spec.ImageID,
		MachineType:      sm.Spec.MachineType,
		AvailabilityZone: sm.Spec.AvailabilityZone,
		SSHKeyName:       sm.Spec.SSHKeyName,
		NetworkID:        sm.Spec.Network.ID,
		SecurityGroups:   sm.Spec.SecurityGroups,
		UserData:         userData,
		Tags:             tags,
		RootVolume: cloud.RootVolumeInput{
			SizeGiB:             sm.Spec.RootVolume.SizeGiB,
			PerformanceClass:    sm.Spec.RootVolume.PerformanceClass,
			DeleteOnTermination: deleteOnTermination,
		},
	})
}

func (r *StackitMachineReconciler) reconcileBastionNodeSSHAccess(
	ctx context.Context,
	c cloud.Client,
	s *scope.MachineScope,
	server *cloud.Server,
) error {
	if !s.StackitCluster.Spec.Bastion.Enabled {
		return nil
	}
	if s.StackitCluster.Status.Bastion.SecurityGroupID == "" {
		return fmt.Errorf("%w: bastion security group ID is empty", cloud.ErrTransient)
	}
	if server == nil || server.ID == "" {
		return fmt.Errorf("%w: server ID is empty", cloud.ErrTransient)
	}
	_, err := c.EnsureNodeSSHAccess(ctx, cloud.NodeSSHAccessInput{
		Name:                   s.StackitCluster.Name + "-node-ssh",
		ServerID:               server.ID,
		BastionSecurityGroupID: s.StackitCluster.Status.Bastion.SecurityGroupID,
		Tags:                   ctrlhlp.NodeSSHAccessTags(s.StackitCluster),
	})
	return err
}

func (r *StackitMachineReconciler) reconcileAPIServerLoadBalancerTarget(
	ctx context.Context,
	c cloud.Client,
	s *scope.MachineScope,
	server *cloud.Server,
) error {
	if !isControlPlaneMachine(s.Machine) || !s.StackitCluster.Spec.APIServerLoadBalancer.Enabled {
		return nil
	}
	loadBalancerID, err := ctrlhlp.EnsureAPIServerLoadBalancerForMachine(
		ctx,
		c,
		s.StackitCluster,
		s.Machine.Name,
		server.Addresses,
	)
	if err != nil {
		return err
	}

	target, err := ctrlhlp.APIServerLoadBalancerTargetForMachine(s.Machine.Name, server.Addresses)
	if err != nil {
		return err
	}
	target.LoadBalancerID = loadBalancerID
	return c.EnsureAPIServerLoadBalancerTarget(ctx, target)
}

func (r *StackitMachineReconciler) deleteAPIServerLoadBalancerTarget(ctx context.Context, c cloud.Client, s *scope.MachineScope) error {
	if !isControlPlaneMachine(s.Machine) || !s.StackitCluster.Spec.APIServerLoadBalancer.Enabled {
		return nil
	}
	loadBalancerID, err := ctrlhlp.ResolveAPIServerLoadBalancerID(ctx, c, s.StackitCluster)
	if err != nil {
		return err
	}
	if loadBalancerID == "" {
		return nil
	}
	err = c.DeleteAPIServerLoadBalancerTarget(ctx, cloud.LoadBalancerTargetInput{
		LoadBalancerID: loadBalancerID,
		Name:           s.Machine.Name,
		Port:           defaultAPIServerPort,
	})
	if cloud.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *StackitMachineReconciler) getStackitCluster(ctx context.Context, cluster *clusterv1.Cluster) (*infrav1.StackitCluster, error) {
	if cluster.Spec.InfrastructureRef.Name == "" {
		return nil, nil
	}
	stackitCluster := &infrav1.StackitCluster{}
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
	if err := r.Get(ctx, key, stackitCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get StackitCluster %s: %w", key, err)
	}
	return stackitCluster, nil
}

func machineAddressesFromCloud(in []cloud.Address) []clusterv1.MachineAddress {
	if len(in) == 0 {
		return nil
	}

	out := make([]clusterv1.MachineAddress, len(in))
	for i, address := range in {
		out[i] = clusterv1.MachineAddress{
			Type:    clusterv1.MachineAddressType(address.Type),
			Address: address.Address,
		}
	}
	return out
}

func isControlPlaneMachine(machine *clusterv1.Machine) bool {
	if machine == nil {
		return false
	}
	_, ok := machine.Labels[clusterv1.MachineControlPlaneLabel]
	return ok
}

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
