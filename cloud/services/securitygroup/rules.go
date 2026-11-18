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
	"net/netip"
	"slices"
	"strings"

	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) ensureSecurityGroupRules(ctx context.Context, securityGroupID string, rules []Rule) error {
	if securityGroupID == "" {
		return fmt.Errorf("%w: security group ID is required", shared.ErrInvalidInput)
	}
	existingRules, err := s.listSecurityGroupRules(ctx, securityGroupID)
	if err != nil {
		return err
	}
	desired := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		rule, err := s.resolveRule(ctx, rule)
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
		if err := s.createSecurityGroupRule(ctx, securityGroupID, rule); err != nil {
			return err
		}
	}

	for _, existing := range existingRules {
		if slices.ContainsFunc(desired, ruleFromSDK(existing).equal) {
			continue
		}
		if err := s.deleteSecurityGroupRule(ctx, securityGroupID, existing.GetId()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveRule(ctx context.Context, rule Rule) (Rule, error) {
	rule = rule.normalize()
	if rule.RemoteSecurityGroupID == "" && len(rule.RemoteSecurityGroupTags) > 0 {
		remoteSG, err := s.findByTags(ctx, rule.RemoteSecurityGroupTags)
		if err != nil {
			if shared.IsNotFound(err) {
				return Rule{}, fmt.Errorf("%w: remote security group not ready: %v", shared.ErrTransient, err)
			}
			return Rule{}, err
		}
		rule.RemoteSecurityGroupID = remoteSG.ID
	}
	rule.RemoteSecurityGroupTags = nil
	return rule, nil
}

func (s *Service) createSecurityGroupRule(ctx context.Context, securityGroupID string, rule Rule) error {
	if _, err := s.scope.IaaSClient().DefaultAPI.CreateSecurityGroupRule(ctx, s.scope.ProjectID(), s.scope.Region(), securityGroupID).
		CreateSecurityGroupRulePayload(rule.toCreatePayload()).
		Execute(); err != nil {
		return shared.ClassifySDKError("create security group rule", err)
	}
	return nil
}

func (s *Service) listSecurityGroupRules(ctx context.Context, securityGroupID string) ([]iaas.SecurityGroupRule, error) {
	resp, err := s.scope.IaaSClient().DefaultAPI.ListSecurityGroupRules(ctx, s.scope.ProjectID(), s.scope.Region(), securityGroupID).Execute()
	if err != nil {
		return nil, shared.ClassifySDKError("list security group rules", err)
	}
	return resp.GetItems(), nil
}

func (s *Service) deleteSecurityGroupRule(ctx context.Context, securityGroupID, ruleID string) error {
	if ruleID == "" {
		return nil
	}
	if err := s.scope.IaaSClient().DefaultAPI.
		DeleteSecurityGroupRule(ctx, s.scope.ProjectID(), s.scope.Region(), securityGroupID, ruleID).
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
	hasRemoteSecurityGroup := r.RemoteSecurityGroupID != "" || len(r.RemoteSecurityGroupTags) > 0
	if r.Direction == DirectionIngress && r.IPRange == "" && !hasRemoteSecurityGroup {
		return fmt.Errorf(
			"%w: ingress security group rule requires ipRange or remoteSecurityGroupID",
			shared.ErrInvalidInput,
		)
	}
	if r.IPRange != "" {
		if hasRemoteSecurityGroup {
			return fmt.Errorf(
				"%w: security group rule cannot specify both ipRange and remoteSecurityGroupID",
				shared.ErrInvalidInput,
			)
		}
		if _, err := netip.ParsePrefix(r.IPRange); err != nil {
			return fmt.Errorf("%w: security group rule has invalid ipRange CIDR %q", shared.ErrInvalidInput, r.IPRange)
		}
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
