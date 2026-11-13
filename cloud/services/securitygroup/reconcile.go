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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	for _, spec := range s.scope.SecurityGroupSpecs() {
		if err := s.client.reconcileSecurityGroup(ctx, spec); err != nil {
			return err
		}
	}
	log.V(1).Info("Reconciled security group resources")
	return nil
}

func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	var errs []error
	for _, spec := range slices.Backward(s.scope.SecurityGroupSpecs()) {
		if err := s.client.deleteSecurityGroup(ctx, spec); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	log.V(1).Info("Deleted security group resources")
	return nil
}

// get retrieves a STACKIT security group by ID.
func (c *client) get(ctx context.Context, id string) (*SecurityGroup, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: security group ID is empty", shared.ErrInvalidInput)
	}
	securityGroup, err := c.scope.IaaSClient().DefaultAPI.GetSecurityGroup(ctx, c.scope.ProjectID(), c.scope.Region(), id).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("get security group", err)
	}
	return securityGroupFromSDK(securityGroup), nil
}

// findByTags finds a single STACKIT security group matching the given tag selector.
func (c *client) findByTags(ctx context.Context, tags map[string]string) (*SecurityGroup, error) {
	return shared.FindByTags(ctx, tags, c.listByTags, "security group", "security groups")
}

// listByTags lists STACKIT security groups matching the given tag selector.
func (c *client) listByTags(ctx context.Context, tags map[string]string) ([]*SecurityGroup, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := c.scope.IaaSClient().DefaultAPI.ListSecurityGroups(ctx, c.scope.ProjectID(), c.scope.Region()).
		LabelSelector(shared.LabelSelector(tags)).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list security groups", err)
	}
	matched := []*SecurityGroup{}
	for _, securityGroup := range resp.GetItems() {
		if shared.LabelsContainTags(securityGroup.GetLabels(), tags) {
			securityGroup := securityGroup
			matched = append(matched, securityGroupFromSDK(&securityGroup))
		}
	}
	return matched, nil
}

// attachToServer attaches a security group to a server idempotently.
// Three error responses are tolerated:
//   - conflict: already attached
//   - invalid input: duplicate item in security_groups list
//   - not found: server has no network port yet
func (c *client) attachToServer(ctx context.Context, serverID, securityGroupID string) error {
	if err := c.scope.IaaSClient().DefaultAPI.
		AddSecurityGroupToServer(ctx, c.scope.ProjectID(), c.scope.Region(), serverID, securityGroupID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("add security group to server", err)
		if !shared.IsConflict(err) && !shared.IsInvalidInput(err) && !shared.IsNotFound(err) {
			return err
		}
	}
	logf.FromContext(ctx).V(1).Info("Attached security group to server", "securityGroupID", securityGroupID, "serverID", serverID)
	return nil
}

// detachFromServer detaches a security group from a server, ignoring NotFound and Conflict errors.
func (c *client) detachFromServer(ctx context.Context, serverID, securityGroupID string) error {
	if err := c.scope.IaaSClient().DefaultAPI.
		RemoveSecurityGroupFromServer(ctx, c.scope.ProjectID(), c.scope.Region(), serverID, securityGroupID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("remove security group from server", err)
		if !shared.IsNotFound(err) && !shared.IsConflict(err) {
			return err
		}
	}
	logf.FromContext(ctx).V(1).Info("Detached security group from server", "securityGroupID", securityGroupID, "serverID", serverID)
	return nil
}

func (c *client) reconcileSecurityGroup(ctx context.Context, spec SecurityGroupSpec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	securityGroup, err := c.ensureSecurityGroup(ctx, spec)
	if err != nil {
		return err
	}
	if spec.SetID != nil {
		spec.SetID(securityGroup.ID)
	}
	if err := c.ensureSecurityGroupRules(ctx, securityGroup.ID, spec.Rules); err != nil {
		return err
	}
	for _, serverID := range spec.ServerIDs {
		if serverID == "" {
			continue
		}
		if err := c.attachToServer(ctx, serverID, securityGroup.ID); err != nil {
			return err
		}
	}
	return nil
}

type resolvedSecurityGroup struct {
	id    string
	rules []iaas.SecurityGroupRule
}

func (c *client) deleteSecurityGroup(ctx context.Context, spec SecurityGroupSpec) error {
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: security group ID or tags are required", shared.ErrInvalidInput)
	}

	resolved, resolveErr := c.resolveSecurityGroupsForDelete(ctx, spec)
	if resolveErr != nil && len(resolved) == 0 {
		return resolveErr
	}
	var errs []error
	if resolveErr != nil {
		errs = append(errs, resolveErr)
	}
	for _, sg := range resolved {
		if err := c.detachSecurityGroupFromServers(ctx, sg.id, spec); err != nil {
			errs = append(errs, err)
		}
		if err := c.deleteSecurityGroupRules(ctx, sg.id, sg.rules); err != nil {
			errs = append(errs, err)
		}
	}
	for _, sg := range resolved {
		if err := c.deleteSecurityGroupByID(ctx, sg.id); err != nil {
			errs = append(errs, err)
			continue
		}
		logf.FromContext(ctx).V(1).Info("Deleted security group", "securityGroupID", sg.id)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if spec.SetID != nil {
		spec.SetID("")
	}
	return nil
}

func (c *client) resolveSecurityGroupsForDelete(
	ctx context.Context,
	spec SecurityGroupSpec,
) ([]resolvedSecurityGroup, error) {
	if spec.ID != "" {
		rules, ok, err := c.listRulesIfGroupExists(ctx, spec.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			return []resolvedSecurityGroup{{id: spec.ID, rules: rules}}, nil
		}
	}
	if len(spec.Tags) == 0 {
		return nil, nil
	}
	securityGroups, err := c.listByTags(ctx, spec.Tags)
	if err != nil {
		return nil, err
	}
	resolved := make([]resolvedSecurityGroup, 0, len(securityGroups))
	var errs []error
	for _, sg := range securityGroups {
		rules, ok, err := c.listRulesIfGroupExists(ctx, sg.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			resolved = append(resolved, resolvedSecurityGroup{id: sg.ID, rules: rules})
		}
	}
	return resolved, errors.Join(errs...)
}

func (c *client) listRulesIfGroupExists(ctx context.Context, id string) ([]iaas.SecurityGroupRule, bool, error) {
	rules, err := c.listSecurityGroupRules(ctx, id)
	if shared.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return rules, true, nil
}

func (c *client) ensureSecurityGroup(ctx context.Context, spec SecurityGroupSpec) (*SecurityGroup, error) {
	if spec.ID != "" {
		securityGroup, err := c.get(ctx, spec.ID)
		if err == nil {
			return securityGroup, nil
		}
		if !shared.IsNotFound(err) {
			return nil, err
		}
	}
	if existing, err := c.findByTags(ctx, spec.Tags); err == nil {
		return existing, nil
	} else if !shared.IsNotFound(err) {
		return nil, err
	}
	securityGroup, err := c.scope.IaaSClient().DefaultAPI.CreateSecurityGroup(ctx, c.scope.ProjectID(), c.scope.Region()).
		CreateSecurityGroupPayload(spec.toCreatePayload()).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("create security group", err)
	}
	out := securityGroupFromSDK(securityGroup)
	log := logf.FromContext(ctx)
	log.V(1).Info("Created security group", "name", spec.Name, "securityGroupID", out.ID)
	return out, nil
}

func (c *client) deleteSecurityGroupRules(
	ctx context.Context,
	id string,
	rules []iaas.SecurityGroupRule,
) error {
	var errs []error
	for _, rule := range rules {
		if err := c.deleteSecurityGroupRule(ctx, id, rule.GetId()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *client) deleteSecurityGroupByID(ctx context.Context, id string) error {
	if err := c.scope.IaaSClient().DefaultAPI.
		DeleteSecurityGroup(ctx, c.scope.ProjectID(), c.scope.Region(), id).
		Execute(); err != nil {
		err := shared.ClassifySDKError("delete security group", err)
		if !shared.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *client) detachSecurityGroupFromServers(ctx context.Context, securityGroupID string, spec SecurityGroupSpec) error {
	var errs []error
	if err := c.detachServers(ctx, securityGroupID, spec.ServerIDs); err != nil {
		errs = append(errs, err)
	}
	if len(spec.ServerSelector) == 0 {
		return errors.Join(errs...)
	}
	serverIDs, err := shared.ListServerIDsByTags(ctx, c.scope, spec.ServerSelector)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := c.detachServers(ctx, securityGroupID, serverIDs); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *client) detachServers(ctx context.Context, securityGroupID string, serverIDs []string) error {
	var errs []error
	for _, serverID := range serverIDs {
		if serverID == "" {
			continue
		}
		if err := c.detachFromServer(ctx, serverID, securityGroupID); err != nil {
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
