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
	"strings"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (c *Client) ensureSecurityGroupRules(ctx context.Context, securityGroupID string, rules []Rule) error {
	if securityGroupID == "" {
		return fmt.Errorf("%w: security group ID is required", shared.ErrInvalidInput)
	}
	existingRules, err := c.listSecurityGroupRules(ctx, securityGroupID)
	if err != nil {
		return err
	}
	desired := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		rule, err := c.resolveRule(ctx, rule)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(desired, rule.equal) {
			continue
		}
		desired = append(desired, rule)
		if hasRule(existingRules, rule) {
			continue
		}
		if err := c.createSecurityGroupRule(ctx, securityGroupID, rule); err != nil {
			return err
		}
	}

	for _, existing := range existingRules {
		if slices.ContainsFunc(desired, ruleFromSDK(existing).equal) {
			continue
		}
		if err := c.deleteSecurityGroupRule(ctx, securityGroupID, existing.GetId()); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) resolveRule(ctx context.Context, rule Rule) (Rule, error) {
	rule = rule.normalize()
	if rule.RemoteSecurityGroupID == "" && len(rule.RemoteSecurityGroupTags) > 0 {
		remoteSG, err := c.FindByTags(ctx, rule.RemoteSecurityGroupTags)
		if err != nil {
			return Rule{}, err
		}
		rule.RemoteSecurityGroupID = remoteSG.ID
	}
	rule.RemoteSecurityGroupTags = nil
	return rule, nil
}

func (c *Client) createSecurityGroupRule(ctx context.Context, securityGroupID string, rule Rule) error {
	if _, err := c.iaasClient.DefaultAPI.CreateSecurityGroupRule(ctx, c.projectID, c.region, securityGroupID).
		CreateSecurityGroupRulePayload(rule.toCreatePayload()).
		Execute(); err != nil {
		return shared.ClassifySDKError("create security group rule", err)
	}
	return nil
}

func (c *Client) listSecurityGroupRules(ctx context.Context, securityGroupID string) ([]iaas.SecurityGroupRule, error) {
	resp, err := c.iaasClient.DefaultAPI.ListSecurityGroupRules(ctx, c.projectID, c.region, securityGroupID).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list security group rules", err)
	}
	return resp.GetItems(), nil
}

func (c *Client) deleteSecurityGroupRule(ctx context.Context, securityGroupID, ruleID string) error {
	if ruleID == "" {
		return nil
	}
	if err := c.iaasClient.DefaultAPI.
		DeleteSecurityGroupRule(ctx, c.projectID, c.region, securityGroupID, ruleID).
		Execute(); err != nil {
		err := shared.ClassifySDKError("delete security group rule", err)
		if !shared.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r Rule) validate() error {
	if r.Direction != DirectionIngress && r.Direction != DirectionEgress {
		return fmt.Errorf(
			"%w: security group rule direction must be %q or %q",
			shared.ErrInvalidInput,
			DirectionIngress,
			DirectionEgress,
		)
	}
	if r.IPRange != "" && (r.RemoteSecurityGroupID != "" || len(r.RemoteSecurityGroupTags) > 0) {
		return fmt.Errorf(
			"%w: security group rule cannot specify both ipRange and remoteSecurityGroupID",
			shared.ErrInvalidInput,
		)
	}
	if r.hasInvalidPortRange() {
		return fmt.Errorf("%w: invalid security group rule port range", shared.ErrInvalidInput)
	}
	if r.hasPortRange() && r.Protocol == "" {
		return fmt.Errorf("%w: security group rule port range requires a protocol", shared.ErrInvalidInput)
	}
	return nil
}

func (r Rule) hasPortRange() bool {
	return r.PortMin > 0 || r.PortMax > 0
}

func (r Rule) hasInvalidPortRange() bool {
	return r.PortMin < 0 || r.PortMax < 0 || r.PortMin > r.PortMax
}

func (p Protocol) normalize() Protocol {
	return Protocol(strings.ToLower(string(p)))
}

func (r Rule) normalize() Rule {
	r.Protocol = r.Protocol.normalize()
	return r
}

func (r Rule) equal(other Rule) bool {
	return r.Direction == other.Direction &&
		r.Protocol == other.Protocol &&
		r.PortMin == other.PortMin &&
		r.PortMax == other.PortMax &&
		r.IPRange == other.IPRange &&
		r.RemoteSecurityGroupID == other.RemoteSecurityGroupID
}

func (r Rule) toCreatePayload() iaas.CreateSecurityGroupRulePayload {
	payload := iaas.NewCreateSecurityGroupRulePayload(string(r.Direction))
	if r.Protocol != "" {
		protocol := iaas.StringAsCreateProtocol(new(string(r.Protocol)))
		payload.SetProtocol(protocol)
	}
	if r.hasPortRange() {
		payload.SetPortRange(*iaas.NewPortRange(r.PortMin, r.PortMax))
	}
	if r.IPRange != "" {
		payload.SetIpRange(r.IPRange)
	}
	if r.RemoteSecurityGroupID != "" {
		payload.SetRemoteSecurityGroupId(r.RemoteSecurityGroupID)
	}
	return *payload
}

func hasRule(rules []iaas.SecurityGroupRule, desired Rule) bool {
	return slices.ContainsFunc(rules, func(rule iaas.SecurityGroupRule) bool {
		return ruleFromSDK(rule).equal(desired)
	})
}

func ruleFromSDK(in iaas.SecurityGroupRule) Rule {
	var ruleProtocol Protocol
	if protocol, ok := in.GetProtocolOk(); ok {
		ruleProtocol = Protocol(protocol.GetName()).normalize()
	}
	var portMin, portMax int64
	if portRange, ok := in.GetPortRangeOk(); ok {
		portMin = portRange.GetMin()
		portMax = portRange.GetMax()
	}
	return Rule{
		Direction:             Direction(in.GetDirection()),
		Protocol:              ruleProtocol,
		PortMin:               portMin,
		PortMax:               portMax,
		IPRange:               in.GetIpRange(),
		RemoteSecurityGroupID: in.GetRemoteSecurityGroupId(),
	}
}
