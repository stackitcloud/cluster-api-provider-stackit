/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package securitygroup

import (
	"context"
	"errors"
	"fmt"
	"slices"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) (ctrl.Result, error) {
	if err := s.ReconcilePrimitive(ctx); err != nil {
		log := logf.FromContext(ctx)
		log.Error(err, "Failed to configure bastion SSH access for node", "machine", s.scope.Name())
		s.scope.Warnf(
			"FailedNodeSSHAccess",
			"Failed to configure bastion SSH access for server %s: %v", firstServerID(s.scope.SecurityGroupSpecs()), err,
		)
		return s.scope.CloudFailureResult(
			"BastionSSHAccessError",
			err,
			shared.RetryableErrorRequeueAfter,
			true,
			infrav1.MachineReadyCondition,
		)
	}
	return ctrl.Result{}, nil
}

func (s *Service) Delete(ctx context.Context) (ctrl.Result, error) {
	if err := s.DeletePrimitive(ctx); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// ReconcilePrimitive reconciles the configured security groups without setting
// StackitMachine-specific conditions or events.
func (s *Service) ReconcilePrimitive(ctx context.Context) error {
	log := logf.FromContext(ctx)
	for _, spec := range s.scope.SecurityGroupSpecs() {
		if err := s.reconcileSecurityGroup(ctx, spec); err != nil {
			return err
		}
	}
	log.V(1).Info("Reconciled security group resources")
	return nil
}

// DeletePrimitive deletes the configured security groups in reverse order.
func (s *Service) DeletePrimitive(ctx context.Context) error {
	log := logf.FromContext(ctx)
	var errs []error
	for _, spec := range slices.Backward(s.scope.SecurityGroupSpecs()) {
		if err := s.deleteSecurityGroup(ctx, spec); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	log.V(1).Info("Deleted security group resources")
	return nil
}

func firstServerID(specs []SecurityGroupSpec) string {
	for _, spec := range specs {
		for _, id := range spec.ServerIDs {
			if id != "" {
				return id
			}
		}
	}
	return ""
}

// get retrieves a STACKIT security group by ID.
func (s *Service) get(ctx context.Context, id string) (*SecurityGroup, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: security group ID is empty", shared.ErrInvalidInput)
	}
	securityGroup, err := s.scope.IaaSClient().DefaultAPI.GetSecurityGroup(ctx, s.scope.ProjectID(), s.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get security group", err)
	}
	return securityGroupFromSDK(securityGroup), nil
}

// findByTags finds a single STACKIT security group matching the given tag selector.
func (s *Service) findByTags(ctx context.Context, tags map[string]string) (*SecurityGroup, error) {
	return shared.FindByTags(ctx, tags, s.listByTags, "security group", "security groups")
}

// listByTags lists STACKIT security groups matching the given tag selector.
func (s *Service) listByTags(ctx context.Context, tags map[string]string) ([]*SecurityGroup, error) {
	return shared.ListByTags(tags, func(selector string) ([]iaas.SecurityGroup, error) {
		resp, err := s.scope.IaaSClient().DefaultAPI.ListSecurityGroups(ctx, s.scope.ProjectID(), s.scope.Region()).
			LabelSelector(selector).
			Execute()
		if err != nil {
			return nil, shared.ClassifySDKError("list security groups", err)
		}
		return resp.GetItems(), nil
	}, securityGroupFromSDK)
}

// attachToServer attaches a security group to a server idempotently.
// Three error responses are tolerated:
//   - conflict: already attached
//   - invalid input: duplicate item in security_groups list
//   - not found: server has no network port yet
func (s *Service) attachToServer(ctx context.Context, serverID, securityGroupID string) error {
	if err := s.scope.IaaSClient().DefaultAPI.
		AddSecurityGroupToServer(ctx, s.scope.ProjectID(), s.scope.Region(), serverID, securityGroupID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("add security group to server", err)
		if !shared.IsConflict(err) && !shared.IsInvalidInput(err) && !shared.IsNotFound(err) {
			return err
		}
		logf.FromContext(ctx).V(1).Info("Security group already attached to server", "securityGroupID", securityGroupID, "serverID", serverID, "err", err)
		return nil
	}
	logf.FromContext(ctx).V(1).Info("Attached security group to server", "securityGroupID", securityGroupID, "serverID", serverID)
	return nil
}

// detachFromServer detaches a security group from a server, ignoring NotFound and Conflict errors.
func (s *Service) detachFromServer(ctx context.Context, serverID, securityGroupID string) error {
	if err := s.scope.IaaSClient().DefaultAPI.
		RemoveSecurityGroupFromServer(ctx, s.scope.ProjectID(), s.scope.Region(), serverID, securityGroupID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("remove security group from server", err)
		if !shared.IsNotFound(err) && !shared.IsConflict(err) {
			return err
		}
		logf.FromContext(ctx).V(1).Info("Security group already detached from server", "securityGroupID", securityGroupID, "serverID", serverID, "err", err)
		return nil
	}
	logf.FromContext(ctx).V(1).Info("Detached security group from server", "securityGroupID", securityGroupID, "serverID", serverID)
	return nil
}

// deleteByID deletes a security group by ID, returning true if the security group existed and was deleted,
// or false if it was already absent.
func (s *Service) deleteByID(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: security group ID is empty", shared.ErrInvalidInput)
	}
	if err := s.scope.IaaSClient().DefaultAPI.
		DeleteSecurityGroup(ctx, s.scope.ProjectID(), s.scope.Region(), id).
		Execute(); err != nil {
		err := shared.ClassifySDKError("delete security group", err)
		if shared.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Service) reconcileSecurityGroup(ctx context.Context, spec SecurityGroupSpec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	securityGroup, err := s.ensureSecurityGroup(ctx, spec)
	if err != nil {
		return err
	}
	s.scope.SetSecurityGroup(spec.Name, securityGroup)
	if err := s.ensureSecurityGroupRules(ctx, securityGroup.ID, spec.Rules); err != nil {
		return err
	}
	for _, serverID := range spec.ServerIDs {
		if serverID == "" {
			continue
		}
		if err := s.attachToServer(ctx, serverID, securityGroup.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteSecurityGroup(ctx context.Context, spec SecurityGroupSpec) error {
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: security group ID or tags are required", shared.ErrInvalidInput)
	}

	deletedIDs, err := shared.DeleteByIDOrTags(ctx, spec.ID, spec.Tags, func(ctx context.Context, id string) (bool, error) {
		return s.cleanupAndDeleteByID(ctx, id, spec)
	}, s.listIDsByTags)
	for _, deletedID := range deletedIDs {
		logf.FromContext(ctx).V(1).Info("Deleted security group", "securityGroupID", deletedID)
	}
	if err != nil {
		return err
	}
	s.scope.SetSecurityGroup(spec.Name, nil)
	return nil
}

func (s *Service) cleanupAndDeleteByID(ctx context.Context, id string, spec SecurityGroupSpec) (bool, error) {
	rules, ok, err := s.listRulesIfGroupExists(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	if err := errors.Join(
		s.detachSecurityGroupFromServers(ctx, id, spec),
		s.deleteSecurityGroupRules(ctx, id, rules),
	); err != nil {
		return false, err
	}
	return s.deleteByID(ctx, id)
}

func (s *Service) listIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	return shared.ListIDsByTags(ctx, tags, s.listByTags, func(securityGroup *SecurityGroup) string {
		return securityGroup.ID
	})
}

func (s *Service) listRulesIfGroupExists(ctx context.Context, id string) ([]iaas.SecurityGroupRule, bool, error) {
	rules, err := s.listSecurityGroupRules(ctx, id)
	if shared.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return rules, true, nil
}

func (s *Service) ensureSecurityGroup(ctx context.Context, spec SecurityGroupSpec) (*SecurityGroup, error) {
	securityGroup, err := shared.ResolveExisting(ctx, spec.ID, spec.Tags, s.get, s.findByTags, shared.IsNotFound)
	if err != nil {
		return nil, err
	}
	if securityGroup != nil {
		return securityGroup, nil
	}
	securityGroupResp, err := s.scope.IaaSClient().DefaultAPI.CreateSecurityGroup(ctx, s.scope.ProjectID(), s.scope.Region()).
		CreateSecurityGroupPayload(spec.toCreatePayload()).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create security group", err)
	}
	out := securityGroupFromSDK(securityGroupResp)
	log := logf.FromContext(ctx)
	log.V(1).Info("Created security group", "name", spec.Name, "securityGroupID", out.ID)
	return out, nil
}

func (s *Service) deleteSecurityGroupRules(
	ctx context.Context,
	id string,
	rules []iaas.SecurityGroupRule,
) error {
	var errs []error
	for _, rule := range rules {
		if err := s.deleteSecurityGroupRule(ctx, id, rule.GetId()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) detachSecurityGroupFromServers(ctx context.Context, securityGroupID string, spec SecurityGroupSpec) error {
	var errs []error
	if err := s.detachServers(ctx, securityGroupID, spec.ServerIDs); err != nil {
		errs = append(errs, err)
	}
	if len(spec.ServerSelector) == 0 {
		return errors.Join(errs...)
	}
	serverIDs, err := shared.ListServerIDsByTags(ctx, s.scope, spec.ServerSelector)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := s.detachServers(ctx, securityGroupID, serverIDs); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) detachServers(ctx context.Context, securityGroupID string, serverIDs []string) error {
	var errs []error
	for _, serverID := range serverIDs {
		if serverID == "" {
			continue
		}
		if err := s.detachFromServer(ctx, serverID, securityGroupID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s SecurityGroupSpec) validate() error {
	if s.Name == "" {
		return fmt.Errorf("%w: security group name is required", shared.ErrInvalidInput)
	}
	if len(s.Tags) == 0 {
		return fmt.Errorf("%w: security group tags are required", shared.ErrInvalidInput)
	}
	for _, rule := range s.Rules {
		if err := rule.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s SecurityGroupSpec) toCreatePayload() iaas.CreateSecurityGroupPayload {
	payload := iaas.NewCreateSecurityGroupPayload(s.Name)
	if s.Description != "" {
		payload.SetDescription(s.Description)
	}
	payload.SetLabels(shared.TagsToSDKLabels(s.Tags))
	return *payload
}

func securityGroupFromSDK(in *iaas.SecurityGroup) *SecurityGroup {
	if in == nil {
		return nil
	}
	return &SecurityGroup{
		ID:   in.GetId(),
		Name: in.GetName(),
	}
}
