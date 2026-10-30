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
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
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
	iaasClient     *iaas.APIClient

	nextID int

	// failure injection: if non-nil, the next call returns this error and the
	// field is cleared.
	FailNextCreateServer     error
	FailNextDeleteServer     error
	FailNextGetServer        error
	FailNextFindServer       error
	FailNextEnsureLB         error
	FailNextDeleteLB         error
	FailNextSetTargets       error
	FailNextGetNetwork       error
	FailNextEnsureBastion    error
	FailNextDeleteBastion    error
	FailNextAddSecurityGroup error
	FailNextListAZ           error
	FailNextGetPublicIP      error
	FailNextListPublicIPs    error
	FailNextCreatePublicIP   error
	FailNextDeletePublicIP   error
	FailNextAddPublicIP      error
	FailNextRemovePublicIP   error

	// AvailabilityZones is returned by ListAvailabilityZones if non-empty.
	// Defaults to eu01-1, eu01-2, eu01-3.
	AvailabilityZones []string

	// LastCreateServerInput records the most recent input passed to CreateServer.
	LastCreateServerInput cloud.CreateServerInput

	// LastEnsureBastionInput records the most recent input passed to EnsureBastion.
	LastEnsureBastionInput cloud.BastionInput

	// Before* hooks, if non-nil, run before the call they belong to. They let a
	// test observe API server state at the exact moment a cloud call would
	// happen. Unlike FailNext*, they are not consumed.
	BeforeCreateServer func()
	BeforeGetNetwork   func()

	// CreateServerCalls counts successful CreateServer calls (for idempotency
	// assertions).
	CreateServerCalls     int
	EnsureBastionCalls    int
	AddSecurityGroupCalls int
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

type securityGroupRuleEntry struct {
	id string
	securitygroup.Rule
}

type securityGroupEntry struct {
	securityGroup        *cloud.SecurityGroup
	description          string
	tags                 map[string]string
	allowedCIDRs         []string
	remoteSecurityGroups []string
	rules                map[string]*securityGroupRuleEntry
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
	iaasClient, err := iaas.NewAPIClient(
		config.WithoutAuthentication(),
		config.WithHTTPClient(&http.Client{Transport: roundTripperFunc(c.roundTripIaaS)}),
		config.WithEndpoint("http://stackit.fake.local"),
	)
	if err != nil {
		panic(fmt.Sprintf("create fake iaas client: %v", err))
	}
	c.iaasClient = iaasClient
	return c
}

func (c *Client) ProjectID() string {
	return "00000000-0000-0000-0000-000000000000"
}

func (c *Client) Region() string {
	return "eu01"
}

func (c *Client) IaaSClient() *iaas.APIClient {
	return c.iaasClient
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
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", c.nextID)
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

	if c.BeforeCreateServer != nil {
		c.BeforeCreateServer()
	}
	if err := consume(&c.FailNextCreateServer); err != nil {
		return nil, err
	}
	for _, entry := range c.servers {
		if len(input.Tags) > 0 && mapContains(entry.tags, input.Tags) {
			return cloneServer(entry.server), nil
		}
	}

	id := c.genID()
	c.LastCreateServerInput = input
	zone := input.AvailabilityZone
	if zone == "" {
		zone = "eu01-1"
	}
	server := &cloud.Server{
		ID:               id,
		Name:             input.Name,
		State:            "ACTIVE",
		AvailabilityZone: zone,
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

	if c.BeforeGetNetwork != nil {
		c.BeforeGetNetwork()
	}
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
	securityGroupID := input.SecurityGroupID
	if securityGroupID == "" {
		securityGroupID = idOfSecurityGroup(c.securityGroupByTags(input.Tags))
	}
	for _, serverEntry := range c.servers {
		if len(input.Tags) > 0 && mapContains(serverEntry.tags, input.Tags) {
			if securityGroupID != "" {
				if serverEntry.securityGroups == nil {
					serverEntry.securityGroups = map[string]struct{}{}
				}
				serverEntry.securityGroups[securityGroupID] = struct{}{}
			}
			return &cloud.Bastion{
				ServerID:          serverEntry.server.ID,
				ServerState:       serverEntry.server.State,
				ServerPowerStatus: serverEntry.server.PowerStatus,
				SecurityGroupID:   securityGroupID,
			}, nil
		}
	}

	serverID := c.genID()
	c.LastEnsureBastionInput = input
	zone := input.AvailabilityZone
	if zone == "" {
		zone = "eu01-1"
	}
	server := &cloud.Server{
		ID:               serverID,
		Name:             input.Name,
		State:            "ACTIVE",
		AvailabilityZone: zone,
		Addresses: []cloud.Address{
			{Type: "InternalIP", Address: "10.0.0.20"},
		},
	}
	var sgSet map[string]struct{}
	if securityGroupID != "" {
		sgSet = securityGroupSet([]string{securityGroupID})
	}
	c.servers[serverID] = &serverEntry{
		server:         server,
		tags:           copyTags(input.Tags),
		securityGroups: sgSet,
		userData:       append([]byte(nil), input.CloudInit...),
	}

	c.EnsureBastionCalls++
	return &cloud.Bastion{
		ServerID:        serverID,
		ServerState:     server.State,
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
	for id, entry := range c.servers {
		if len(input.Tags) > 0 && mapContains(entry.tags, input.Tags) {
			delete(c.servers, id)
		}
	}
	return nil
}

func (c *Client) GetPublicIP(_ context.Context, id string) (*cloud.PublicIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextGetPublicIP); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("public IP ID is empty: %w", cloud.ErrInvalidInput)
	}
	entry, ok := c.publicIPs[id]
	if !ok {
		return nil, fmt.Errorf("public IP %q: %w", id, cloud.ErrNotFound)
	}
	out := *entry.publicIP
	return &out, nil
}

func (c *Client) ListPublicIPsByTags(_ context.Context, tags map[string]string) ([]*cloud.PublicIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextListPublicIPs); err != nil {
		return nil, err
	}
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

func (c *Client) CreatePublicIP(_ context.Context, tags map[string]string) (*cloud.PublicIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextCreatePublicIP); err != nil {
		return nil, err
	}
	id := c.genID()
	publicIP := &cloud.PublicIP{
		ID: id,
		IP: "203.0.113.22",
	}
	c.publicIPs[id] = &publicIPEntry{
		publicIP: publicIP,
		tags:     copyTags(tags),
	}
	out := *publicIP
	return &out, nil
}

func (c *Client) DeletePublicIP(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeletePublicIP); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("public IP ID is empty: %w", cloud.ErrInvalidInput)
	}
	if _, ok := c.publicIPs[id]; !ok {
		return fmt.Errorf("public IP %q: %w", id, cloud.ErrNotFound)
	}
	delete(c.publicIPs, id)
	return nil
}

func (c *Client) AddPublicIPToServer(_ context.Context, serverID, publicIPID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextAddPublicIP); err != nil {
		return err
	}
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("server ID and public IP ID are required: %w", cloud.ErrInvalidInput)
	}
	if _, ok := c.servers[serverID]; !ok {
		return fmt.Errorf("server %q: %w", serverID, cloud.ErrNotFound)
	}
	entry, ok := c.publicIPs[publicIPID]
	if !ok {
		return fmt.Errorf("public IP %q: %w", publicIPID, cloud.ErrNotFound)
	}
	entry.publicIP.NetworkInterfaceID = "fake-nic-" + serverID
	return nil
}

func (c *Client) RemovePublicIPFromServer(_ context.Context, serverID, publicIPID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextRemovePublicIP); err != nil {
		return err
	}
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("server ID and public IP ID are required: %w", cloud.ErrInvalidInput)
	}
	entry, ok := c.publicIPs[publicIPID]
	if !ok {
		return fmt.Errorf("public IP %q: %w", publicIPID, cloud.ErrNotFound)
	}
	entry.publicIP.NetworkInterfaceID = ""
	return nil
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

func (c *Client) CreateSecurityGroup(_ context.Context, name string, tags map[string]string) (*cloud.SecurityGroup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.createSecurityGroupEntry(name, "", tags)
	if err != nil {
		return nil, err
	}
	out := *entry.securityGroup
	return &out, nil
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

func (c *Client) SetAPIServerLoadBalancerTargets(
	_ context.Context,
	loadBalancerID string,
	port int32,
	targets []cloud.LoadBalancerTargetInput,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextSetTargets); err != nil {
		return err
	}
	if loadBalancerID == "" || port <= 0 {
		return fmt.Errorf("load balancer ID and target port are required: %w", cloud.ErrInvalidInput)
	}
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required: %w", cloud.ErrInvalidInput)
	}
	entry, ok := c.loadBalancers[loadBalancerID]
	if !ok {
		return fmt.Errorf("load balancer %q: %w", loadBalancerID, cloud.ErrNotFound)
	}
	replaced := make(map[string]string, len(targets))
	for _, target := range targets {
		// The real API rejects these; accepting them would hide a regression.
		if target.Name == "" || target.IP == "" {
			return fmt.Errorf("target name and target IP are required: %w", cloud.ErrInvalidInput)
		}
		replaced[target.Name] = target.IP
	}
	entry.targets = replaced
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

// SetServerState sets the state and power status of a fake server (test helper).
func (c *Client) SetServerState(serverID, state, powerStatus string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.servers[serverID]; ok {
		entry.server.State = state
		entry.server.PowerStatus = powerStatus
	}
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

// SecurityGroupRules returns the rules currently stored on a fake security group.
func (c *Client) SecurityGroupRules(securityGroupID string) []securitygroup.Rule {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.securityGroups[securityGroupID]
	if !ok {
		return nil
	}
	rules := make([]securitygroup.Rule, 0, len(entry.rules))
	for _, r := range entry.rules {
		rules = append(rules, r.Rule)
	}
	return rules
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

// LoadBalancerTargetIPs returns one target pool as name to IP (test helper).
func (c *Client) LoadBalancerTargetIPs(id string) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.loadBalancers[id]
	if !ok {
		return nil
	}
	return copyTags(entry.targets)
}

// SetLoadBalancerIP updates the IP of a load balancer (test helper).
func (c *Client) SetLoadBalancerIP(id, ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.loadBalancers[id]; ok {
		entry.lb.IP = ip
	}
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

func (c *Client) securityGroupByTags(tags map[string]string) *cloud.SecurityGroup {
	for _, entry := range c.securityGroups {
		if mapContains(entry.tags, tags) {
			out := *entry.securityGroup
			return &out
		}
	}
	return nil
}

func idOfSecurityGroup(securityGroup *cloud.SecurityGroup) string {
	if securityGroup == nil {
		return ""
	}
	return securityGroup.ID
}

func (c *Client) ListAvailabilityZones(_ context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := consume(&c.FailNextListAZ); err != nil {
		return nil, err
	}
	if len(c.AvailabilityZones) > 0 {
		return slices.Clone(c.AvailabilityZones), nil
	}
	return []string{"eu01-1", "eu01-2", "eu01-3"}, nil
}

func cloneServer(s *cloud.Server) *cloud.Server {
	out := *s
	if s.Addresses != nil {
		out.Addresses = make([]cloud.Address, len(s.Addresses))
		copy(out.Addresses, s.Addresses)
	}
	return &out
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (c *Client) roundTripIaaS(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")

	if c.handlePublicIPRequest(rec, r) || c.handleSecurityGroupRequest(rec, r) {
		return rec.Result(), nil
	}

	rec.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(rec).Encode(map[string]any{"message": "not found"})
	return rec.Result(), nil
}

func (c *Client) handlePublicIPRequest(rec *httptest.ResponseRecorder, r *http.Request) bool {
	path := r.URL.Path

	switch {
	case strings.Contains(path, "/servers/") && strings.Contains(path, "/public-ips/"):
		parts := strings.Split(path, "/servers/")
		sub := strings.Split(parts[1], "/public-ips/")
		serverID, publicIPID := sub[0], sub[1]
		var err error
		switch r.Method {
		case http.MethodPut:
			err = c.AddPublicIPToServer(r.Context(), serverID, publicIPID)
		case http.MethodDelete:
			err = c.RemovePublicIPFromServer(r.Context(), serverID, publicIPID)
		default:
			err = cloud.ErrInvalidInput
		}
		writeFakeStatus(rec, err)
		return true

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/public-ips"):
		var body struct {
			Labels map[string]string `json:"labels"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		created, err := c.CreatePublicIP(r.Context(), body.Labels)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(fakePublicIPMap(created, body.Labels))
		}
		return true

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/public-ips"):
		tags := parseLabelSelector(r.URL.Query().Get("label_selector"))
		items, err := c.listPublicIPItems(tags)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"items": items})
		}
		return true

	case r.Method == http.MethodGet && strings.Contains(path, "/public-ips/"):
		id := path[strings.LastIndex(path, "/public-ips/")+len("/public-ips/"):]
		got, err := c.GetPublicIP(r.Context(), id)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(fakePublicIPMap(got, nil))
		}
		return true

	case r.Method == http.MethodDelete && strings.Contains(path, "/public-ips/"):
		id := path[strings.LastIndex(path, "/public-ips/")+len("/public-ips/"):]
		writeFakeStatus(rec, c.DeletePublicIP(r.Context(), id))
		return true

	default:
		return false
	}
}

func (c *Client) handleSecurityGroupRequest(rec *httptest.ResponseRecorder, r *http.Request) bool {
	path := r.URL.Path

	switch {
	case strings.Contains(path, "/servers/") && strings.Contains(path, "/security-groups/"):
		parts := strings.Split(path, "/servers/")
		sub := strings.Split(parts[1], "/security-groups/")
		serverID, securityGroupID := sub[0], sub[1]
		var err error
		switch r.Method {
		case http.MethodPut:
			err = c.addSecurityGroupToServer(serverID, securityGroupID)
		case http.MethodDelete:
			err = c.removeSecurityGroupFromServer(serverID, securityGroupID)
		default:
			err = cloud.ErrInvalidInput
		}
		writeFakeStatus(rec, err)
		return true

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
		tags := parseLabelSelector(r.URL.Query().Get("label_selector"))
		items, err := c.listServerItems(tags)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"items": items})
		}
		return true

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/security-groups"):
		var body struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Labels      map[string]string `json:"labels"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		created, err := c.createSecurityGroup(body.Name, body.Description, body.Labels)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(created)
		}
		return true

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/security-groups"):
		tags := parseLabelSelector(r.URL.Query().Get("label_selector"))
		items, err := c.listSecurityGroupItems(tags)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"items": items})
		}
		return true

	case strings.Contains(path, "/security-groups/") && strings.HasSuffix(path, "/rules") && r.Method == http.MethodGet:
		sub := path[strings.Index(path, "/security-groups/")+len("/security-groups/"):]
		sgID := strings.TrimSuffix(sub, "/rules")
		items, err := c.listSecurityGroupRuleItems(sgID)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"items": items})
		}
		return true

	case strings.Contains(path, "/security-groups/") && strings.HasSuffix(path, "/rules") && r.Method == http.MethodPost:
		c.handleCreateSecurityGroupRule(rec, r)
		return true

	case strings.Contains(path, "/security-groups/") && strings.Contains(path, "/rules/") && r.Method == http.MethodDelete:
		sub := path[strings.Index(path, "/security-groups/")+len("/security-groups/"):]
		parts := strings.SplitN(sub, "/rules/", 2)
		writeFakeStatus(rec, c.deleteSecurityGroupRule(parts[0], parts[1]))
		return true

	case r.Method == http.MethodDelete && strings.Contains(path, "/security-groups/"):
		id := path[strings.LastIndex(path, "/security-groups/")+len("/security-groups/"):]
		writeFakeStatus(rec, c.deleteSecurityGroup(id))
		return true

	default:
		return false
	}
}

func (c *Client) handleCreateSecurityGroupRule(rec *httptest.ResponseRecorder, r *http.Request) {
	path := r.URL.Path
	sub := path[strings.Index(path, "/security-groups/")+len("/security-groups/"):]
	sgID := strings.TrimSuffix(sub, "/rules")
	var body struct {
		Direction string `json:"direction"`
		Protocol  string `json:"protocol"`
		PortRange *struct {
			Min int64 `json:"min"`
			Max int64 `json:"max"`
		} `json:"portRange"`
		IPRange               string `json:"ipRange"`
		RemoteSecurityGroupID string `json:"remoteSecurityGroupId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	var portMin, portMax int64
	if body.PortRange != nil {
		portMin = body.PortRange.Min
		portMax = body.PortRange.Max
	}
	created, err := c.createSecurityGroupRule(sgID, securityGroupRuleEntry{
		Rule: securitygroup.Rule{
			Direction:             securitygroup.Direction(body.Direction),
			Protocol:              securitygroup.Protocol(body.Protocol),
			PortMin:               portMin,
			PortMax:               portMax,
			IPRange:               body.IPRange,
			RemoteSecurityGroupID: body.RemoteSecurityGroupID,
		},
	})
	if err != nil {
		writeFakeError(rec, err)
	} else {
		_ = json.NewEncoder(rec).Encode(created)
	}
}

func writeFakeStatus(rec *httptest.ResponseRecorder, err error) {
	if err != nil {
		writeFakeError(rec, err)
	} else {
		rec.WriteHeader(http.StatusNoContent)
	}
}

func (c *Client) listPublicIPItems(tags map[string]string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextListPublicIPs); err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrInvalidInput)
	}
	items := make([]map[string]any, 0, len(c.publicIPs))
	for _, entry := range c.publicIPs {
		if mapContains(entry.tags, tags) {
			items = append(items, fakePublicIPMap(entry.publicIP, entry.tags))
		}
	}
	return items, nil
}

func fakePublicIPMap(p *cloud.PublicIP, tags map[string]string) map[string]any {
	m := map[string]any{
		"id": p.ID,
		"ip": p.IP,
	}
	if p.NetworkInterfaceID != "" {
		m["networkInterface"] = p.NetworkInterfaceID
	}
	if len(tags) > 0 {
		labels := make(map[string]any, len(tags))
		for k, v := range tags {
			labels[k] = v
		}
		m["labels"] = labels
	}
	return m
}

func (c *Client) addSecurityGroupToServer(serverID, securityGroupID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextAddSecurityGroup); err != nil {
		return err
	}
	serverEntry, ok := c.servers[serverID]
	if !ok {
		return fmt.Errorf("server %q: %w", serverID, cloud.ErrNotFound)
	}
	if _, ok := c.securityGroups[securityGroupID]; !ok {
		return fmt.Errorf("security group %q: %w", securityGroupID, cloud.ErrNotFound)
	}
	if serverEntry.securityGroups == nil {
		serverEntry.securityGroups = map[string]struct{}{}
	}
	serverEntry.securityGroups[securityGroupID] = struct{}{}
	c.AddSecurityGroupCalls++
	return nil
}

func (c *Client) listServerItems(tags map[string]string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	items := make([]map[string]any, 0, len(c.servers))
	for _, entry := range c.servers {
		if len(tags) > 0 && !mapContains(entry.tags, tags) {
			continue
		}
		m := map[string]any{
			"id":          entry.server.ID,
			"name":        entry.server.Name,
			"machineType": "c2i.1",
			"status":      entry.server.State,
		}
		if len(entry.tags) > 0 {
			labels := make(map[string]any, len(entry.tags))
			for k, v := range entry.tags {
				labels[k] = v
			}
			m["labels"] = labels
		}
		items = append(items, m)
	}
	return items, nil
}

func (c *Client) removeSecurityGroupFromServer(serverID, securityGroupID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	serverEntry, ok := c.servers[serverID]
	if !ok {
		return fmt.Errorf("server %q: %w", serverID, cloud.ErrNotFound)
	}
	delete(serverEntry.securityGroups, securityGroupID)
	return nil
}

func (c *Client) createSecurityGroup(name, description string, tags map[string]string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.createSecurityGroupEntry(name, description, tags)
	if err != nil {
		return nil, err
	}
	return fakeSecurityGroupMap(entry), nil
}

func (c *Client) createSecurityGroupEntry(name, description string, tags map[string]string) (*securityGroupEntry, error) {
	if name == "" {
		return nil, fmt.Errorf("security group name is empty: %w", cloud.ErrInvalidInput)
	}
	id := c.genID()
	sg := &cloud.SecurityGroup{
		ID:   id,
		Name: name,
	}
	entry := &securityGroupEntry{
		securityGroup: sg,
		description:   description,
		tags:          copyTags(tags),
		rules:         make(map[string]*securityGroupRuleEntry),
	}
	c.securityGroups[id] = entry
	return entry, nil
}

func (c *Client) listSecurityGroupItems(tags map[string]string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(tags) == 0 {
		return nil, fmt.Errorf("empty tags: %w", cloud.ErrInvalidInput)
	}
	items := make([]map[string]any, 0, len(c.securityGroups))
	for _, entry := range c.securityGroups {
		if mapContains(entry.tags, tags) {
			items = append(items, fakeSecurityGroupMap(entry))
		}
	}
	return items, nil
}

func (c *Client) listSecurityGroupRuleItems(sgID string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.securityGroups[sgID]
	if !ok {
		return nil, fmt.Errorf("security group %q: %w", sgID, cloud.ErrNotFound)
	}
	items := make([]map[string]any, 0, len(entry.rules))
	for _, rule := range entry.rules {
		items = append(items, fakeSecurityGroupRuleMap(rule))
	}
	return items, nil
}

func (c *Client) createSecurityGroupRule(sgID string, rule securityGroupRuleEntry) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.securityGroups[sgID]
	if !ok {
		return nil, fmt.Errorf("security group %q: %w", sgID, cloud.ErrNotFound)
	}
	if entry.rules == nil {
		entry.rules = make(map[string]*securityGroupRuleEntry)
	}
	rule.id = c.genID()
	stored := rule
	entry.rules[rule.id] = &stored
	if rule.IPRange != "" && !containsString(entry.allowedCIDRs, rule.IPRange) {
		entry.allowedCIDRs = append(entry.allowedCIDRs, rule.IPRange)
	}
	if rule.RemoteSecurityGroupID != "" && !containsString(entry.remoteSecurityGroups, rule.RemoteSecurityGroupID) {
		entry.remoteSecurityGroups = append(entry.remoteSecurityGroups, rule.RemoteSecurityGroupID)
	}
	return fakeSecurityGroupRuleMap(&stored), nil
}

func (c *Client) deleteSecurityGroupRule(sgID, ruleID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.securityGroups[sgID]
	if !ok {
		return fmt.Errorf("security group %q: %w", sgID, cloud.ErrNotFound)
	}
	if entry.rules == nil {
		return fmt.Errorf("rule %q: %w", ruleID, cloud.ErrNotFound)
	}
	if _, ok := entry.rules[ruleID]; !ok {
		return fmt.Errorf("rule %q: %w", ruleID, cloud.ErrNotFound)
	}
	delete(entry.rules, ruleID)
	return nil
}

func (c *Client) deleteSecurityGroup(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if id == "" {
		return fmt.Errorf("security group ID is empty: %w", cloud.ErrInvalidInput)
	}
	if _, ok := c.securityGroups[id]; !ok {
		return fmt.Errorf("security group %q: %w", id, cloud.ErrNotFound)
	}
	for serverID, server := range c.servers {
		if _, attached := server.securityGroups[id]; attached {
			return fmt.Errorf("security group %q is still attached to server %q: %w", id, serverID, cloud.ErrConflict)
		}
	}
	delete(c.securityGroups, id)
	return nil
}

func fakeSecurityGroupMap(entry *securityGroupEntry) map[string]any {
	m := map[string]any{
		"id":   entry.securityGroup.ID,
		"name": entry.securityGroup.Name,
	}
	if entry.description != "" {
		m["description"] = entry.description
	}
	if len(entry.tags) > 0 {
		labels := make(map[string]any, len(entry.tags))
		for k, v := range entry.tags {
			labels[k] = v
		}
		m["labels"] = labels
	}
	return m
}

func fakeSecurityGroupRuleMap(rule *securityGroupRuleEntry) map[string]any {
	m := map[string]any{
		"id":        rule.id,
		"direction": string(rule.Direction),
	}
	if rule.Protocol != "" {
		m["protocol"] = map[string]any{"name": string(rule.Protocol)}
	}
	if rule.PortMin > 0 || rule.PortMax > 0 {
		m["portRange"] = map[string]any{"min": rule.PortMin, "max": rule.PortMax}
	}
	if rule.IPRange != "" {
		m["ipRange"] = rule.IPRange
	}
	if rule.RemoteSecurityGroupID != "" {
		m["remoteSecurityGroupId"] = rule.RemoteSecurityGroupID
	}
	return m
}

func parseLabelSelector(selector string) map[string]string {
	if selector == "" {
		return nil
	}
	tags := make(map[string]string)
	for part := range strings.SplitSeq(selector, ",") {
		if k, v, ok := strings.Cut(part, "="); ok && k != "" {
			tags[k] = v
		}
	}
	return tags
}

func writeFakeError(rec *httptest.ResponseRecorder, err error) {
	status := http.StatusInternalServerError
	switch {
	case cloud.IsNotFound(err):
		status = http.StatusNotFound
	case cloud.IsConflict(err):
		status = http.StatusConflict
	case cloud.IsInvalidInput(err):
		status = http.StatusBadRequest
	case cloud.IsUnauthorized(err):
		status = http.StatusUnauthorized
	}
	rec.WriteHeader(status)
	_ = json.NewEncoder(rec).Encode(map[string]any{"message": err.Error()})
}
