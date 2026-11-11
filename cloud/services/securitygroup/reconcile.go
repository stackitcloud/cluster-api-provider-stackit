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
	"fmt"
	"slices"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx)
	for _, spec := range s.scope.SecurityGroupSpecs() {
		if err := s.reconcileSecurityGroup(ctx, spec); err != nil {
			return err
		}
	}
	log.V(1).Info("Reconciled security group resources")
	return nil
}

func (s *Service) Delete(ctx context.Context) error {
	log := logf.FromContext(ctx)
	for _, spec := range slices.Backward(s.scope.SecurityGroupSpecs()) {
		if err := s.deleteSecurityGroup(ctx, spec); err != nil {
			return err
		}
	}
	log.V(1).Info("Deleted security group resources")
	return nil
}

// FindByTags finds a single STACKIT security group matching the given tag selector.
func (c *Client) FindByTags(ctx context.Context, tags map[string]string) (*SecurityGroup, error) {
	matched, err := c.ListByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("%w: no security group matching tags", shared.ErrNotFound)
	}
	if len(matched) > 1 {
		return nil, fmt.Errorf("%w: multiple security groups matching tags", shared.ErrConflict)
	}
	return matched[0], nil
}

// ListByTags lists STACKIT security groups matching the given tag selector.
func (c *Client) ListByTags(ctx context.Context, tags map[string]string) ([]*SecurityGroup, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", shared.ErrInvalidInput)
	}
	resp, err := c.iaasClient.DefaultAPI.ListSecurityGroups(ctx, c.projectID, c.region).
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

// DeleteByID deletes a security group and its rules by ID, ignoring NotFound errors.
func (c *Client) DeleteByID(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: security group ID is empty", shared.ErrInvalidInput)
	}
	rules, ok, err := c.listRulesIfGroupExists(ctx, id)
	if err != nil || !ok {
		return err
	}
	return c.deleteSecurityGroupWithRules(ctx, id, rules)
}

// AttachToServer attaches a security group to a server idempotently.
// Three error responses are tolerated:
//   - conflict: already attached
//   - invalid input: duplicate item in security_groups list
//   - not found: server has no network port yet
func (c *Client) AttachToServer(ctx context.Context, serverID, securityGroupID string) error {
	if err := c.iaasClient.DefaultAPI.
		AddSecurityGroupToServer(ctx, c.projectID, c.region, serverID, securityGroupID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("add security group to server", err)
		if !shared.IsConflict(err) && !shared.IsInvalidInput(err) && !shared.IsNotFound(err) {
			return err
		}
	}
	logf.FromContext(ctx).V(1).Info("Attached security group to server", "securityGroupID", securityGroupID, "serverID", serverID)
	return nil
}

// DetachFromServer detaches a security group from a server, ignoring NotFound and Conflict errors.
func (c *Client) DetachFromServer(ctx context.Context, serverID, securityGroupID string) error {
	if err := c.iaasClient.DefaultAPI.
		RemoveSecurityGroupFromServer(ctx, c.projectID, c.region, serverID, securityGroupID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("remove security group from server", err)
		if !shared.IsNotFound(err) && !shared.IsConflict(err) {
			return err
		}
	}
	logf.FromContext(ctx).V(1).Info("Detached security group from server", "securityGroupID", securityGroupID, "serverID", serverID)
	return nil
}

func (c *Client) reconcileSecurityGroup(ctx context.Context, spec SecurityGroupSpec) error {
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
		if err := c.AttachToServer(ctx, serverID, securityGroup.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) deleteSecurityGroup(ctx context.Context, spec SecurityGroupSpec) error {
	if spec.ID == "" && len(spec.Tags) == 0 {
		return fmt.Errorf("%w: security group ID or tags are required", shared.ErrInvalidInput)
	}

	securityGroupID, rules, err := c.resolveSecurityGroupForDelete(ctx, spec)
	if err != nil {
		return err
	}
	if securityGroupID != "" {
		if err := c.detachSecurityGroupFromServers(ctx, securityGroupID, spec); err != nil {
			return err
		}
		if err := c.deleteSecurityGroupWithRules(ctx, securityGroupID, rules); err != nil {
			return err
		}
		logf.FromContext(ctx).V(1).Info("Deleted security group", "securityGroupID", securityGroupID)
	}
	if spec.SetID != nil {
		spec.SetID("")
	}
	return nil
}

func (c *Client) resolveSecurityGroupForDelete(
	ctx context.Context,
	spec SecurityGroupSpec,
) (string, []iaas.SecurityGroupRule, error) {
	if spec.ID != "" {
		rules, ok, err := c.listRulesIfGroupExists(ctx, spec.ID)
		if err != nil || ok {
			return spec.ID, rules, err
		}
	}
	if len(spec.Tags) == 0 {
		return "", nil, nil
	}
	securityGroup, err := c.FindByTags(ctx, spec.Tags)
	if shared.IsNotFound(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	rules, _, err := c.listRulesIfGroupExists(ctx, securityGroup.ID)
	return securityGroup.ID, rules, err
}

func (c *Client) listRulesIfGroupExists(ctx context.Context, id string) ([]iaas.SecurityGroupRule, bool, error) {
	rules, err := c.listSecurityGroupRules(ctx, id)
	if shared.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return rules, true, nil
}

func (c *Client) ensureSecurityGroup(ctx context.Context, spec SecurityGroupSpec) (*SecurityGroup, error) {
	if existing, err := c.FindByTags(ctx, spec.Tags); err == nil {
		return existing, nil
	} else if !shared.IsNotFound(err) {
		return nil, err
	}
	securityGroup, err := c.iaasClient.DefaultAPI.CreateSecurityGroup(ctx, c.projectID, c.region).
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

func (c *Client) deleteSecurityGroupWithRules(
	ctx context.Context,
	id string,
	rules []iaas.SecurityGroupRule,
) error {
	for _, rule := range rules {
		if err := c.deleteSecurityGroupRule(ctx, id, rule.GetId()); err != nil {
			return err
		}
	}
	if err := c.iaasClient.DefaultAPI.
		DeleteSecurityGroup(ctx, c.projectID, c.region, id).
		Execute(); err != nil {
		err := shared.ClassifySDKError("delete security group", err)
		if !shared.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Client) detachSecurityGroupFromServers(ctx context.Context, securityGroupID string, spec SecurityGroupSpec) error {
	if err := c.detachServers(ctx, securityGroupID, spec.ServerIDs); err != nil {
		return err
	}
	if len(spec.ServerSelector) == 0 {
		return nil
	}
	serverIDs, err := c.listServerIDsByTags(ctx, spec.ServerSelector)
	if err != nil {
		return err
	}
	return c.detachServers(ctx, securityGroupID, serverIDs)
}

func (c *Client) detachServers(ctx context.Context, securityGroupID string, serverIDs []string) error {
	for _, serverID := range serverIDs {
		if serverID == "" {
			continue
		}
		if err := c.DetachFromServer(ctx, serverID, securityGroupID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) listServerIDsByTags(ctx context.Context, tags map[string]string) ([]string, error) {
	resp, err := c.iaasClient.DefaultAPI.ListServers(ctx, c.projectID, c.region).
		LabelSelector(shared.LabelSelector(tags)).
		Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list servers", err)
	}
	var serverIDs []string
	for _, server := range resp.GetItems() {
		if shared.LabelsContainTags(server.GetLabels(), tags) && server.GetId() != "" {
			serverIDs = append(serverIDs, server.GetId())
		}
	}
	return serverIDs, nil
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
