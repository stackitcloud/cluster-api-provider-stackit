/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package fake provides an in-memory cloud.Client used by unit and envtest
// tests. It supports failure injection so callers can exercise error paths.
package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
)

const (
	bootstrapTargetName = "capi-bootstrap-placeholder"
	bootstrapTargetIP   = "10.0.0.1"
)

// Client is an in-memory implementation of cloud.Client.
type Client struct {
	mu sync.Mutex

	servers        map[string]*serverEntry
	loadBalancers  map[string]*lbEntry
	publicIPs      map[string]*publicIPEntry
	securityGroups map[string]*securityGroupEntry
	networks       map[string]*cloud.Network

	nextID int

	// failure injection: if non-nil, the next call returns this error and the
	// field is cleared.
	FailNextCreateServer  error
	FailNextDeleteServer  error
	FailNextGetServer     error
	FailNextFindServer    error
	FailNextEnsureLB      error
	FailNextDeleteLB      error
	FailNextEnsureTarget  error
	FailNextDeleteTarget  error
	FailNextGetNetwork    error
	FailNextEnsureBastion error
	FailNextDeleteBastion error
	FailNextEnsureNodeSSH error
	FailNextDeleteNodeSSH error

	// CreateServerCalls counts successful CreateServer calls (for idempotency
	// assertions).
	CreateServerCalls  int
	EnsureBastionCalls int
	EnsureNodeSSHCalls int
}

type serverEntry struct {
	server         *cloud.Server
	tags           map[string]string
	securityGroups map[string]struct{}
	userData       []byte
}

type lbEntry struct {
	lb      *cloud.LoadBalancer
	tags    map[string]string
	targets map[string]string
}

type publicIPEntry struct {
	publicIP *cloud.PublicIP
	tags     map[string]string
}

type securityGroupEntry struct {
	securityGroup        *cloud.SecurityGroup
	tags                 map[string]string
	allowedCIDRs         []string
	remoteSecurityGroups []string
}

// New returns a Client preconfigured with the given networks.
func New(networks ...cloud.Network) *Client {
	c := &Client{
		servers:        make(map[string]*serverEntry),
		loadBalancers:  make(map[string]*lbEntry),
		publicIPs:      make(map[string]*publicIPEntry),
		securityGroups: make(map[string]*securityGroupEntry),
		networks:       make(map[string]*cloud.Network),
	}
	for i := range networks {
		n := networks[i]
		c.networks[n.ID] = &n
	}
	return c
}

func consume(p *error) error {
	if *p == nil {
		return nil
	}
	err := *p
	*p = nil
	return err
}

func (c *Client) genID() string {
	c.nextID++
	return fmt.Sprintf("fake-%d", c.nextID)
}

func (c *Client) GetServer(_ context.Context, id string) (*cloud.Server, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextGetServer); err != nil {
		return nil, err
	}
	entry, ok := c.servers[id]
	if !ok {
		return nil, fmt.Errorf("server %q: %w", id, cloud.ErrNotFound)
	}
	return cloneServer(entry.server), nil
}

func (c *Client) FindServerByTags(_ context.Context, tags map[string]string) (*cloud.Server, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextFindServer); err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrNotFound)
	}
	for _, entry := range c.servers {
		if mapContains(entry.tags, tags) {
			return cloneServer(entry.server), nil
		}
	}
	return nil, fmt.Errorf("no server matching tags: %w", cloud.ErrNotFound)
}

func (c *Client) ListServersByTags(_ context.Context, tags map[string]string) ([]*cloud.Server, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrInvalidInput)
	}
	servers := []*cloud.Server{}
	for _, entry := range c.servers {
		if mapContains(entry.tags, tags) {
			servers = append(servers, cloneServer(entry.server))
		}
	}
	return servers, nil
}

// CreateServer creates a server. To satisfy spec section 20 (idempotency),
// it returns the existing server if one with matching tags already exists.
func (c *Client) CreateServer(_ context.Context, input cloud.CreateServerInput) (*cloud.Server, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextCreateServer); err != nil {
		return nil, err
	}
	for _, entry := range c.servers {
		if len(input.Tags) > 0 && mapContains(entry.tags, input.Tags) {
			return cloneServer(entry.server), nil
		}
	}

	id := c.genID()
	server := &cloud.Server{
		ID:    id,
		Name:  input.Name,
		State: "ACTIVE",
		Addresses: []cloud.Address{
			{Type: "InternalIP", Address: "10.0.0.10"},
		},
	}
	c.servers[id] = &serverEntry{
		server:         server,
		tags:           copyTags(input.Tags),
		securityGroups: securityGroupSet(input.SecurityGroups),
		userData:       append([]byte(nil), input.UserData...),
	}
	c.CreateServerCalls++
	return cloneServer(server), nil
}

// DeleteServer removes a server. It returns nil if the server is already gone.
func (c *Client) DeleteServer(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteServer); err != nil {
		return err
	}
	delete(c.servers, id)
	return nil
}

func (c *Client) GetNetwork(_ context.Context, id string) (*cloud.Network, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextGetNetwork); err != nil {
		return nil, err
	}
	n, ok := c.networks[id]
	if !ok {
		return nil, fmt.Errorf("network %q: %w", id, cloud.ErrNotFound)
	}
	out := *n
	return &out, nil
}

func (c *Client) EnsureBastion(_ context.Context, input cloud.BastionInput) (*cloud.Bastion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextEnsureBastion); err != nil {
		return nil, err
	}
	for _, serverEntry := range c.servers {
		if len(input.Tags) > 0 && mapContains(serverEntry.tags, input.Tags) {
			publicIP := c.publicIPByTags(input.Tags)
			securityGroup := c.securityGroupByTags(input.Tags)
			return &cloud.Bastion{
				ServerID:        serverEntry.server.ID,
				ServerState:     serverEntry.server.State,
				PublicIPID:      idOfPublicIP(publicIP),
				PublicIP:        ipOfPublicIP(publicIP),
				SecurityGroupID: idOfSecurityGroup(securityGroup),
			}, nil
		}
	}

	securityGroupID := c.genID()
	securityGroup := &cloud.SecurityGroup{
		ID:   securityGroupID,
		Name: input.Name + "-ssh",
	}
	c.securityGroups[securityGroupID] = &securityGroupEntry{
		securityGroup: securityGroup,
		tags:          copyTags(input.Tags),
		allowedCIDRs:  append([]string(nil), input.AllowedCIDRs...),
	}

	serverID := c.genID()
	server := &cloud.Server{
		ID:    serverID,
		Name:  input.Name,
		State: "ACTIVE",
		Addresses: []cloud.Address{
			{Type: "InternalIP", Address: "10.0.0.20"},
		},
	}
	c.servers[serverID] = &serverEntry{
		server:         server,
		tags:           copyTags(input.Tags),
		securityGroups: securityGroupSet([]string{securityGroupID}),
		userData:       append([]byte(nil), input.CloudInit...),
	}

	publicIPID := c.genID()
	publicIP := &cloud.PublicIP{
		ID:                 publicIPID,
		IP:                 "203.0.113.22",
		NetworkInterfaceID: "fake-nic-" + serverID,
	}
	c.publicIPs[publicIPID] = &publicIPEntry{publicIP: publicIP, tags: copyTags(input.Tags)}

	c.EnsureBastionCalls++
	return &cloud.Bastion{
		ServerID:        serverID,
		ServerState:     server.State,
		PublicIPID:      publicIPID,
		PublicIP:        publicIP.IP,
		SecurityGroupID: securityGroupID,
	}, nil
}

func (c *Client) DeleteBastion(_ context.Context, input cloud.BastionInput, status cloud.Bastion) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteBastion); err != nil {
		return err
	}
	if status.ServerID != "" {
		delete(c.servers, status.ServerID)
	}
	if status.PublicIPID != "" {
		delete(c.publicIPs, status.PublicIPID)
	}
	if status.SecurityGroupID != "" {
		delete(c.securityGroups, status.SecurityGroupID)
	}
	for id, entry := range c.servers {
		if mapContains(entry.tags, input.Tags) {
			delete(c.servers, id)
		}
	}
	for id, entry := range c.publicIPs {
		if mapContains(entry.tags, input.Tags) {
			delete(c.publicIPs, id)
		}
	}
	for id, entry := range c.securityGroups {
		if mapContains(entry.tags, input.Tags) {
			delete(c.securityGroups, id)
		}
	}
	return nil
}

func (c *Client) EnsureNodeSSHAccess(_ context.Context, input cloud.NodeSSHAccessInput) (*cloud.SecurityGroup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextEnsureNodeSSH); err != nil {
		return nil, err
	}
	serverEntry, ok := c.servers[input.ServerID]
	if !ok {
		return nil, fmt.Errorf("server %q: %w", input.ServerID, cloud.ErrNotFound)
	}
	securityGroup := c.securityGroupByTags(input.Tags)
	if securityGroup == nil {
		securityGroup = &cloud.SecurityGroup{
			ID:   c.genID(),
			Name: input.Name,
		}
		c.securityGroups[securityGroup.ID] = &securityGroupEntry{
			securityGroup:        securityGroup,
			tags:                 copyTags(input.Tags),
			remoteSecurityGroups: []string{input.BastionSecurityGroupID},
		}
	} else {
		entry := c.securityGroups[securityGroup.ID]
		if !containsString(entry.remoteSecurityGroups, input.BastionSecurityGroupID) {
			entry.remoteSecurityGroups = append(entry.remoteSecurityGroups, input.BastionSecurityGroupID)
		}
	}
	if serverEntry.securityGroups == nil {
		serverEntry.securityGroups = map[string]struct{}{}
	}
	serverEntry.securityGroups[securityGroup.ID] = struct{}{}
	c.EnsureNodeSSHCalls++
	out := *securityGroup
	return &out, nil
}

func (c *Client) DeleteNodeSSHAccess(_ context.Context, tags map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteNodeSSH); err != nil {
		return err
	}
	for id, entry := range c.securityGroups {
		if mapContains(entry.tags, tags) {
			delete(c.securityGroups, id)
			for _, server := range c.servers {
				delete(server.securityGroups, id)
			}
		}
	}
	return nil
}

func (c *Client) ListPublicIPsByTags(_ context.Context, tags map[string]string) ([]*cloud.PublicIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrInvalidInput)
	}
	publicIPs := []*cloud.PublicIP{}
	for _, entry := range c.publicIPs {
		if mapContains(entry.tags, tags) {
			out := *entry.publicIP
			publicIPs = append(publicIPs, &out)
		}
	}
	return publicIPs, nil
}

func (c *Client) ListSecurityGroupsByTags(_ context.Context, tags map[string]string) ([]*cloud.SecurityGroup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrInvalidInput)
	}
	securityGroups := []*cloud.SecurityGroup{}
	for _, entry := range c.securityGroups {
		if mapContains(entry.tags, tags) {
			out := *entry.securityGroup
			securityGroups = append(securityGroups, &out)
		}
	}
	return securityGroups, nil
}

func (c *Client) EnsureAPIServerLoadBalancer(
	_ context.Context,
	input cloud.LoadBalancerInput,
) (*cloud.LoadBalancer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextEnsureLB); err != nil {
		return nil, err
	}
	for _, entry := range c.loadBalancers {
		if len(input.Tags) > 0 && mapContains(entry.tags, input.Tags) {
			out := *entry.lb
			return &out, nil
		}
	}
	id := c.genID()
	lb := &cloud.LoadBalancer{
		ID:   id,
		Name: input.Name,
		IP:   "203.0.113.10",
		Port: input.Port,
	}
	targets := map[string]string{}
	if len(input.Targets) == 0 {
		targets[bootstrapTargetName] = bootstrapTargetIP
	} else {
		for _, target := range input.Targets {
			targets[target.Name] = target.IP
		}
	}
	c.loadBalancers[id] = &lbEntry{lb: lb, tags: copyTags(input.Tags), targets: targets}
	out := *lb
	return &out, nil
}

func (c *Client) ListAPIServerLoadBalancersByTags(
	_ context.Context,
	tags map[string]string,
) ([]*cloud.LoadBalancer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrInvalidInput)
	}
	loadBalancers := []*cloud.LoadBalancer{}
	for _, entry := range c.loadBalancers {
		if mapContains(entry.tags, tags) {
			out := *entry.lb
			loadBalancers = append(loadBalancers, &out)
		}
	}
	return loadBalancers, nil
}

func (c *Client) EnsureAPIServerLoadBalancerTarget(_ context.Context, input cloud.LoadBalancerTargetInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextEnsureTarget); err != nil {
		return err
	}
	entry, ok := c.loadBalancers[input.LoadBalancerID]
	if !ok {
		return fmt.Errorf("load balancer %q: %w", input.LoadBalancerID, cloud.ErrNotFound)
	}
	if entry.targets[bootstrapTargetName] == bootstrapTargetIP {
		delete(entry.targets, bootstrapTargetName)
	}
	entry.targets[input.Name] = input.IP
	return nil
}

func (c *Client) DeleteAPIServerLoadBalancerTarget(_ context.Context, input cloud.LoadBalancerTargetInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteTarget); err != nil {
		return err
	}
	entry, ok := c.loadBalancers[input.LoadBalancerID]
	if !ok {
		return fmt.Errorf("load balancer %q: %w", input.LoadBalancerID, cloud.ErrNotFound)
	}
	delete(entry.targets, input.Name)
	return nil
}

func (c *Client) DeleteAPIServerLoadBalancer(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteLB); err != nil {
		return err
	}
	delete(c.loadBalancers, id)
	return nil
}

// ServerCount returns the number of currently tracked servers (test helper).
func (c *Client) ServerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.servers)
}

// LoadBalancerCount returns the number of currently tracked load balancers
// (test helper).
func (c *Client) LoadBalancerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.loadBalancers)
}

// PublicIPCount returns the number of currently tracked public IPs.
func (c *Client) PublicIPCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.publicIPs)
}

// SecurityGroupCount returns the number of currently tracked security groups.
func (c *Client) SecurityGroupCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.securityGroups)
}

// ServerHasSecurityGroup reports whether the server has the security group
// attached.
func (c *Client) ServerHasSecurityGroup(serverID, securityGroupID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.servers[serverID]
	if !ok {
		return false
	}
	_, ok = entry.securityGroups[securityGroupID]
	return ok
}

// ServerUserData returns the user-data stored for a fake server.
func (c *Client) ServerUserData(serverID string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.servers[serverID]
	if !ok {
		return nil
	}
	return append([]byte(nil), entry.userData...)
}

// SecurityGroupRemoteSources returns the remote security groups allowed by the
// fake security group rules.
func (c *Client) SecurityGroupRemoteSources(securityGroupID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.securityGroups[securityGroupID]
	if !ok {
		return nil
	}
	return append([]string(nil), entry.remoteSecurityGroups...)
}

// LoadBalancerTargetCount returns the number of targets in one load balancer
// target pool (test helper).
func (c *Client) LoadBalancerTargetCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.loadBalancers[id]
	if !ok {
		return 0
	}
	return len(entry.targets)
}

func mapContains(haystack, needle map[string]string) bool {
	for k, v := range needle {
		if haystack[k] != v {
			return false
		}
	}
	return true
}

func copyTags(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func securityGroupSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func containsString(values []string, needle string) bool {
	return slices.Contains(values, needle)
}

func (c *Client) publicIPByTags(tags map[string]string) *cloud.PublicIP {
	for _, entry := range c.publicIPs {
		if mapContains(entry.tags, tags) {
			out := *entry.publicIP
			return &out
		}
	}
	return nil
}

func (c *Client) securityGroupByTags(tags map[string]string) *cloud.SecurityGroup {
	for _, entry := range c.securityGroups {
		if mapContains(entry.tags, tags) {
			out := *entry.securityGroup
			return &out
		}
	}
	return nil
}

func idOfPublicIP(publicIP *cloud.PublicIP) string {
	if publicIP == nil {
		return ""
	}
	return publicIP.ID
}

func ipOfPublicIP(publicIP *cloud.PublicIP) string {
	if publicIP == nil {
		return ""
	}
	return publicIP.IP
}

func idOfSecurityGroup(securityGroup *cloud.SecurityGroup) string {
	if securityGroup == nil {
		return ""
	}
	return securityGroup.ID
}

func cloneServer(s *cloud.Server) *cloud.Server {
	out := *s
	if s.Addresses != nil {
		out.Addresses = make([]cloud.Address, len(s.Addresses))
		copy(out.Addresses, s.Addresses)
	}
	return &out
}
