/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	spec := s.scope.ServerSpec()
	srv, created, err := s.ReconcilePrimitive(ctx)
	if IsServerGone(err) {
		s.scope.SetNotReady(
			"InstanceNotFound",
			err.Error(),
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
		s.scope.Warnf(
			"InstanceNotFound",
			"Server %s no longer exists; the Machine must be replaced", spec.ID,
		)
		return s.scope.ServerGoneResult(err)
	}
	if err != nil {
		log.Error(err, "Failed to create server",
			"machine", s.scope.Name(),
			"imageID", spec.ImageID,
			"networkID", spec.NetworkID,
			"region", s.scope.Region(),
		)
		s.scope.Warnf(
			"FailedCreateInstance",
			"Failed to create server %s (imageID: %s, networkID: %s, region: %s): %v",
			s.scope.Name(), spec.ImageID, spec.NetworkID, s.scope.Region(), err,
		)
		return s.scope.CloudFailureResult(
			"InstanceError",
			err,
			shared.RetryableErrorRequeueAfter,
			true,
			infrav1.MachineInstanceReadyCondition,
			infrav1.MachineReadyCondition,
		)
	}
	if created {
		s.scope.Eventf("InstanceCreated", "Created instance %s", srv.ID)
	}

	if ready, reason, message, warn := StateCondition(srv.State, srv.PowerStatus); !ready {
		previousReason := ""
		if previous := s.scope.FindCondition(infrav1.MachineInstanceReadyCondition); previous != nil {
			previousReason = previous.Reason
		}
		s.scope.SetNotReady(reason, message, infrav1.MachineInstanceReadyCondition, infrav1.MachineReadyCondition)
		// Only warn on entering the state, not on every requeue.
		if warn && previousReason != reason {
			s.scope.Warnf(reason, "Server %s: %s", srv.ID, message)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (s *Service) Delete(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	instanceID := s.scope.ServerSpec().ID
	if err := s.DeletePrimitive(ctx); err != nil {
		log.Error(err, "Failed to delete server", "machine", s.scope.Name(), "instanceID", instanceID)
		s.scope.Warnf("FailedDeleteInstance",
			"Failed to delete server %s: %v", instanceID, err)
		return ctrl.Result{}, err
	}
	if instanceID != "" {
		s.scope.Eventf("InstanceDeleted", "Deleted instance %s", instanceID)
	}
	return ctrl.Result{}, nil
}

// ReconcilePrimitive reconciles the server and updates the scope without setting
// StackitMachine-specific conditions or events.
func (s *Service) ReconcilePrimitive(ctx context.Context) (*Server, bool, error) {
	log := logf.FromContext(ctx)
	spec := s.scope.ServerSpec()
	if err := spec.validate(); err != nil {
		return nil, false, err
	}

	srv, created, err := s.ensureServer(ctx, spec)
	if err != nil {
		return nil, false, err
	}
	s.scope.SetServer(srv, created)
	log.V(1).Info("Reconciled server resources", "serverID", srv.ID, "created", created)
	return srv, created, nil
}

// DeletePrimitive deletes the server matching ServerSpec without emitting
// StackitMachine-specific events.
func (s *Service) DeletePrimitive(ctx context.Context) error {
	log := logf.FromContext(ctx)
	spec := s.scope.ServerSpec()
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: server ID or tags are required", shared.ErrInvalidInput)
	}

	deletedIDs, err := shared.DeleteByIDOrTags(ctx, spec.ID, spec.Tags, s.deleteByID, func(ctx context.Context, tags map[string]string) ([]string, error) {
		return shared.ListServerIDsByTags(ctx, s.scope, tags)
	})
	for _, deletedID := range deletedIDs {
		log.V(1).Info("Deleted server", "serverID", deletedID)
	}
	if err != nil {
		return err
	}
	s.scope.SetServer(nil, false)
	return nil
}

// get retrieves a STACKIT server by ID.
func (s *Service) get(ctx context.Context, id string) (*Server, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: server ID is empty", shared.ErrInvalidInput)
	}
	srv, err := s.scope.IaaSClient().DefaultAPI.GetServer(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get server", err)
	}
	return s.serverFromSDK(ctx, srv), nil
}

// findByTags finds a single STACKIT server matching the given tag selector.
func (s *Service) findByTags(ctx context.Context, tags map[string]string) (*Server, error) {
	srv, err := shared.FindByTags(ctx, tags, func(ctx context.Context, tags map[string]string) ([]*iaas.Server, error) {
		return shared.ListServersByTags(ctx, s.scope, tags)
	}, "server", "servers")
	if err != nil {
		return nil, err
	}
	return s.serverFromSDK(ctx, srv), nil
}

// deleteByID deletes a server by ID, returning true if the server existed and was deleted,
// or false if it was already absent.
func (s *Service) deleteByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: server ID is empty", shared.ErrInvalidInput)
	}
	if err := s.scope.IaaSClient().DefaultAPI.DeleteServer(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute(); err != nil {
		err := shared.ClassifySDKError("delete server", err)
		if shared.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// create creates a new STACKIT server from the given spec.
func (s *Service) create(ctx context.Context, spec ServerSpec) (*Server, error) {
	payload := spec.toCreatePayload()
	created, err := s.scope.IaaSClient().DefaultAPI.CreateServer(ctx, s.scope.ProjectID(), s.scope.Region()).
		CreateServerPayload(payload).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create server", err)
	}
	out := s.serverFromSDK(ctx, created)
	logf.FromContext(ctx).V(1).Info("Created server", "name", spec.Name, "serverID", out.ID)
	return out, nil
}

func (s *Service) ensureServer(ctx context.Context, spec ServerSpec) (*Server, bool, error) {
	srv, err := shared.ResolveExisting(ctx, spec.ID, spec.Tags, s.get, s.findByTags, shared.IsNotFound)
	if err != nil {
		return nil, false, err
	}
	if srv != nil {
		return srv, false, nil
	}

	// Recreating an already-provisioned server would replay bootstrap data
	// pinned to the previous identity, so surface the loss and let Cluster API
	// replace the Machine.
	if spec.DisallowRecreate {
		return nil, false, fmt.Errorf(
			"%w: server %s; the Machine must be replaced",
			errServerGone, spec.ID,
		)
	}

	out, err := s.create(ctx, spec)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (s ServerSpec) validate() error {
	if s.Name == "" {
		return fmt.Errorf("%w: server name is required", shared.ErrInvalidInput)
	}
	if s.ImageID == "" {
		return fmt.Errorf("%w: server image ID is required", shared.ErrInvalidInput)
	}
	if s.MachineType == "" {
		return fmt.Errorf("%w: server machine type is required", shared.ErrInvalidInput)
	}
	if s.RequireSSHKey && s.SSHKeyName == "" {
		return fmt.Errorf("%w: server SSH key name is required", shared.ErrInvalidInput)
	}
	if s.NetworkID == "" {
		return fmt.Errorf("%w: server network ID is required", shared.ErrInvalidInput)
	}
	if len(s.Tags) == 0 {
		return fmt.Errorf("%w: server tags are required", shared.ErrInvalidInput)
	}
	return nil
}

func (s ServerSpec) toCreatePayload() iaas.CreateServerPayload {
	payload := &iaas.CreateServerPayload{}
	payload.SetName(s.Name)
	payload.SetMachineType(s.MachineType)
	useBootVolume := s.RootVolume.SizeGiB > 0 || s.RootVolume.PerformanceClass != ""
	if s.ImageID != "" && !useBootVolume {
		payload.SetImageId(s.ImageID)
	}
	if len(s.Tags) > 0 {
		payload.SetLabels(shared.TagsToSDKLabels(s.Tags))
	}
	networking := iaas.NewCreateServerNetworking()
	if s.NetworkID != "" {
		networking.SetNetworkId(s.NetworkID)
	}
	payload.SetNetworking(iaas.CreateServerNetworkingAsCreateServerPayloadAllOfNetworking(networking))
	if len(s.SecurityGroups) > 0 {
		payload.SetSecurityGroups(s.SecurityGroups)
	}
	if len(s.UserData) > 0 {
		payload.SetUserData(base64.StdEncoding.EncodeToString(s.UserData))
		payload.SetConfigDrive(true)
	}
	if useBootVolume {
		bootVolume := iaas.NewBootVolume()
		if s.ImageID != "" {
			bootVolume.SetSource(*iaas.NewBootVolumeSource(s.ImageID, "image"))
		}
		if s.RootVolume.SizeGiB > 0 {
			bootVolume.SetSize(int64(s.RootVolume.SizeGiB))
		}
		if s.RootVolume.PerformanceClass != "" {
			bootVolume.SetPerformanceClass(s.RootVolume.PerformanceClass)
		}
		bootVolume.SetDeleteOnTermination(s.RootVolume.DeleteOnTermination)
		payload.SetBootVolume(*bootVolume)
	}
	if s.SSHKeyName != "" {
		payload.SetKeypairName(s.SSHKeyName)
	}
	if s.AvailabilityZone != "" {
		payload.SetAvailabilityZone(s.AvailabilityZone)
	}
	return *payload
}

func (s *Service) serverFromSDK(ctx context.Context, srv *iaas.Server) *Server {
	if srv == nil {
		return nil
	}
	out := &Server{
		ID:               srv.GetId(),
		Name:             srv.GetName(),
		State:            srv.GetStatus(),
		PowerStatus:      srv.GetPowerStatus(),
		AvailabilityZone: srv.GetAvailabilityZone(),
	}
	nics, err := s.scope.IaaSClient().DefaultAPI.ListServerNICs(ctx, s.scope.ProjectID(), s.scope.Region(), out.ID).Execute()
	if err == nil {
		for _, nic := range nics.GetItems() {
			if ipv4 := nic.GetIpv4(); ipv4 != "" {
				out.Addresses = append(out.Addresses, Address{Type: "InternalIP", Address: ipv4})
			}
			if ipv6 := nic.GetIpv6(); ipv6 != "" {
				out.Addresses = append(out.Addresses, Address{Type: "InternalIP", Address: ipv6})
			}
		}
	}
	return out
}
