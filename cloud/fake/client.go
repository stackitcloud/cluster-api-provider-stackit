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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/network"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

// Client is an in-memory implementation of cloud.Client.
type Client struct {
	mu sync.Mutex

	servers        map[string]*serverEntry
	loadBalancers  map[string]*lbEntry
	publicIPs      map[string]*publicIPEntry
	securityGroups map[string]*securityGroupEntry
	networks       map[string]*network.Network
	iaasClient     *iaas.APIClient
	lbClient       *lb.APIClient

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

	// LastCreateServerInput records the most recent spec passed to CreateServer.
	LastCreateServerInput server.ServerSpec

	// Before* hooks, if non-nil, run before the call they belong to. They let a
	// test observe API server state at the exact moment a cloud call would
	// happen. Unlike FailNext*, they are not consumed.
	BeforeCreateServer func()
	BeforeGetNetwork   func()

	// CreateServerCalls counts successful CreateServer calls (for idempotency
	// assertions).
	CreateServerCalls     int
	AddSecurityGroupCalls int
}

type serverEntry struct {
	server         *server.Server
	tags           map[string]string
	securityGroups map[string]struct{}
	userData       []byte
}

type lbEntry struct {
	lb      *loadbalancer.LoadBalancer
	tags    map[string]string
	targets map[string]string
}

type publicIPEntry struct {
	publicIP *publicip.PublicIP
	tags     map[string]string
}

type securityGroupRuleEntry struct {
	id string
	securitygroup.Rule
}

type securityGroupEntry struct {
	securityGroup *securitygroup.SecurityGroup
	description   string
	tags          map[string]string
	rules         map[string]*securityGroupRuleEntry
}

// New returns a Client preconfigured with the given networks.
func New(networks ...network.Network) *Client {
	c := &Client{
		servers:        make(map[string]*serverEntry),
		loadBalancers:  make(map[string]*lbEntry),
		publicIPs:      make(map[string]*publicIPEntry),
		securityGroups: make(map[string]*securityGroupEntry),
		networks:       make(map[string]*network.Network),
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

	lbClient, err := lb.NewAPIClient(
		config.WithoutAuthentication(),
		config.WithHTTPClient(&http.Client{Transport: roundTripperFunc(c.roundTripLB)}),
		config.WithEndpoint("http://stackit.fake.local"),
	)
	if err != nil {
		panic(fmt.Sprintf("create fake loadbalancer client: %v", err))
	}
	c.lbClient = lbClient
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

func (c *Client) LoadBalancerClient() *lb.APIClient {
	return c.lbClient
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

func (c *Client) createServerEntry(input server.ServerSpec) *serverEntry {
	id := c.genID()
	c.LastCreateServerInput = input
	zone := input.AvailabilityZone
	if zone == "" {
		zone = "eu01-1"
	}
	srv := &server.Server{
		ID:               id,
		Name:             input.Name,
		State:            "ACTIVE",
		AvailabilityZone: zone,
		Addresses: []server.Address{
			{Type: "InternalIP", Address: "10.0.0.10"},
		},
	}
	entry := &serverEntry{
		server:         srv,
		tags:           copyTags(input.Tags),
		securityGroups: securityGroupSet(input.SecurityGroups),
		userData:       append([]byte(nil), input.UserData...),
	}
	c.servers[id] = entry
	c.CreateServerCalls++
	return entry
}

func (c *Client) GetNetwork(_ context.Context, id string) (*network.Network, error) {
	c.mu.Lock()
	if c.BeforeGetNetwork != nil {
		c.mu.Unlock()
		c.BeforeGetNetwork()
		c.mu.Lock()
	}
	defer c.mu.Unlock()

	if err := consume(&c.FailNextGetNetwork); err != nil {
		return nil, err
	}
	n, ok := c.networks[id]
	if !ok {
		return nil, fmt.Errorf("network %q: %w", id, shared.ErrNotFound)
	}
	out := *n
	return &out, nil
}

func (c *Client) getPublicIP(_ context.Context, id string) (*publicip.PublicIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextGetPublicIP); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("public IP ID is empty: %w", shared.ErrInvalidInput)
	}
	entry, ok := c.publicIPs[id]
	if !ok {
		return nil, fmt.Errorf("public IP %q: %w", id, shared.ErrNotFound)
	}
	out := *entry.publicIP
	return &out, nil
}

func (c *Client) CreatePublicIP(_ context.Context, tags map[string]string) (*publicip.PublicIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextCreatePublicIP); err != nil {
		return nil, err
	}
	id := c.genID()
	publicIP := &publicip.PublicIP{
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

func (c *Client) deletePublicIP(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeletePublicIP); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("public IP ID is empty: %w", shared.ErrInvalidInput)
	}
	if _, ok := c.publicIPs[id]; !ok {
		return fmt.Errorf("public IP %q: %w", id, shared.ErrNotFound)
	}
	delete(c.publicIPs, id)
	return nil
}

func (c *Client) addPublicIPToServer(_ context.Context, serverID, publicIPID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextAddPublicIP); err != nil {
		return err
	}
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("server ID and public IP ID are required: %w", shared.ErrInvalidInput)
	}
	if _, ok := c.servers[serverID]; !ok {
		return fmt.Errorf("server %q: %w", serverID, shared.ErrNotFound)
	}
	entry, ok := c.publicIPs[publicIPID]
	if !ok {
		return fmt.Errorf("public IP %q: %w", publicIPID, shared.ErrNotFound)
	}
	entry.publicIP.NetworkInterfaceID = "fake-nic-" + serverID
	return nil
}

func (c *Client) removePublicIPFromServer(_ context.Context, serverID, publicIPID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextRemovePublicIP); err != nil {
		return err
	}
	if serverID == "" || publicIPID == "" {
		return fmt.Errorf("server ID and public IP ID are required: %w", shared.ErrInvalidInput)
	}
	entry, ok := c.publicIPs[publicIPID]
	if !ok {
		return fmt.Errorf("public IP %q: %w", publicIPID, shared.ErrNotFound)
	}
	entry.publicIP.NetworkInterfaceID = ""
	return nil
}

func (c *Client) CreateSecurityGroup(_ context.Context, name string, tags map[string]string) (*securitygroup.SecurityGroup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.createSecurityGroupEntry(name, "", tags)
	if err != nil {
		return nil, err
	}
	out := *entry.securityGroup
	return &out, nil
}

func (c *Client) CreateLoadBalancer(_ context.Context, name string, tags map[string]string) (*loadbalancer.LoadBalancer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.createLoadBalancerEntry(name, 6443, loadbalancer.Labels(tags), nil)
	if err != nil {
		return nil, err
	}
	out := *entry.lb
	return &out, nil
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (c *Client) roundTripIaaS(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")

	if c.handlePublicIPRequest(rec, r) ||
		c.handleSecurityGroupRequest(rec, r) ||
		c.handleServerRequest(rec, r) ||
		c.handleNetworkRequest(rec, r) ||
		c.handleAvailabilityZoneRequest(rec, r) {
		return rec.Result(), nil
	}

	rec.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(rec).Encode(map[string]any{"message": "not found"})
	return rec.Result(), nil
}

func (c *Client) handleNetworkRequest(rec *httptest.ResponseRecorder, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/networks/") {
		return false
	}
	id := r.URL.Path[strings.LastIndex(r.URL.Path, "/networks/")+len("/networks/"):]
	n, err := c.GetNetwork(r.Context(), id)
	if err != nil {
		writeFakeError(rec, err)
		return true
	}
	m := map[string]any{
		"id":     n.ID,
		"name":   n.Name,
		"status": "CREATED",
		"routed": true,
	}
	if len(n.IPv4Prefixes) > 0 {
		m["ipv4"] = map[string]any{
			"prefixes": slices.Clone(n.IPv4Prefixes),
		}
	}
	_ = json.NewEncoder(rec).Encode(m)
	return true
}

func (c *Client) handleAvailabilityZoneRequest(rec *httptest.ResponseRecorder, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/availability-zones") {
		return false
	}
	zones, err := c.ListAvailabilityZones(r.Context())
	if err != nil {
		writeFakeError(rec, err)
		return true
	}
	_ = json.NewEncoder(rec).Encode(map[string]any{"items": zones})
	return true
}

func (c *Client) roundTripLB(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")

	if c.handleLoadBalancerRequest(rec, r) {
		return rec.Result(), nil
	}

	rec.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(rec).Encode(map[string]any{"message": "not found"})
	return rec.Result(), nil
}

type fakeTargetJSON struct {
	DisplayName string `json:"displayName"`
	IP          string `json:"ip"`
}

func toLoadBalancerTargets(in []fakeTargetJSON) []loadbalancer.Target {
	out := make([]loadbalancer.Target, 0, len(in))
	for _, t := range in {
		out = append(out, loadbalancer.Target{Name: t.DisplayName, IP: t.IP})
	}
	return out
}

func (c *Client) handleLoadBalancerRequest(rec *httptest.ResponseRecorder, r *http.Request) bool {
	path := r.URL.Path

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/load-balancers"):
		items, err := c.listLoadBalancerItems()
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"loadBalancers": items})
		}
		return true

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/load-balancers"):
		var body struct {
			Name        string            `json:"name"`
			Labels      map[string]string `json:"labels"`
			TargetPools []struct {
				Name       string           `json:"name"`
				TargetPort int32            `json:"targetPort"`
				Targets    []fakeTargetJSON `json:"targets"`
			} `json:"targetPools"`
			Listeners []struct {
				Port int32 `json:"port"`
			} `json:"listeners"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		var port int32 = 6443
		if len(body.Listeners) > 0 && body.Listeners[0].Port > 0 {
			port = body.Listeners[0].Port
		}
		var targets []loadbalancer.Target
		if len(body.TargetPools) > 0 {
			targets = toLoadBalancerTargets(body.TargetPools[0].Targets)
		}
		created, err := c.createLoadBalancer(body.Name, port, body.Labels, targets)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(created)
		}
		return true

	case r.Method == http.MethodPut && strings.Contains(path, "/load-balancers/") && strings.Contains(path, "/target-pools/"):
		sub := path[strings.Index(path, "/load-balancers/")+len("/load-balancers/"):]
		parts := strings.SplitN(sub, "/target-pools/", 2)
		lbID := parts[0]
		var body struct {
			Name       string           `json:"name"`
			TargetPort int32            `json:"targetPort"`
			Targets    []fakeTargetJSON `json:"targets"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		pool, err := c.updateLoadBalancerTargetPool(lbID, body.TargetPort, toLoadBalancerTargets(body.Targets))
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(pool)
		}
		return true

	case r.Method == http.MethodGet && strings.Contains(path, "/load-balancers/"):
		id := path[strings.LastIndex(path, "/load-balancers/")+len("/load-balancers/"):]
		got, err := c.getLoadBalancerItem(id)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(got)
		}
		return true

	case r.Method == http.MethodDelete && strings.Contains(path, "/load-balancers/"):
		id := path[strings.LastIndex(path, "/load-balancers/")+len("/load-balancers/"):]
		if err := c.deleteLoadBalancer(id); err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{})
		}
		return true

	default:
		return false
	}
}

func (c *Client) listLoadBalancerItems() ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	items := make([]map[string]any, 0, len(c.loadBalancers))
	for _, entry := range c.loadBalancers {
		items = append(items, fakeLoadBalancerMap(entry))
	}
	return items, nil
}

func (c *Client) getLoadBalancerItem(id string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextSetTargets); err != nil {
		return nil, err
	}
	entry, ok := c.loadBalancers[id]
	if !ok {
		return nil, fmt.Errorf("load balancer %q: %w", id, shared.ErrNotFound)
	}
	return fakeLoadBalancerMap(entry), nil
}

func (c *Client) createLoadBalancer(name string, port int32, labels map[string]string, targets []loadbalancer.Target) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextEnsureLB); err != nil {
		return nil, err
	}
	entry, err := c.createLoadBalancerEntry(name, port, labels, targets)
	if err != nil {
		return nil, err
	}
	return fakeLoadBalancerMap(entry), nil
}

func (c *Client) createLoadBalancerEntry(name string, port int32, labels map[string]string, targets []loadbalancer.Target) (*lbEntry, error) {
	if name == "" {
		return nil, fmt.Errorf("load balancer name is empty: %w", shared.ErrInvalidInput)
	}
	if port <= 0 {
		port = 6443
	}
	lbObj := &loadbalancer.LoadBalancer{
		ID:   name,
		Name: name,
		IP:   "203.0.113.10",
		Port: port,
	}
	targetMap := map[string]string{}
	if len(targets) == 0 {
		bootstrap := loadbalancer.BootstrapTarget("")
		targetMap[bootstrap.Name] = bootstrap.IP
	} else {
		for _, target := range targets {
			targetMap[target.Name] = target.IP
		}
	}
	entry := &lbEntry{
		lb:      lbObj,
		tags:    copyTags(labels),
		targets: targetMap,
	}
	c.loadBalancers[name] = entry
	return entry, nil
}

func (c *Client) updateLoadBalancerTargetPool(id string, port int32, targets []loadbalancer.Target) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextSetTargets); err != nil {
		return nil, err
	}
	if id == "" || port <= 0 {
		return nil, fmt.Errorf("load balancer ID and target port are required: %w", shared.ErrInvalidInput)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one target is required: %w", shared.ErrInvalidInput)
	}
	entry, ok := c.loadBalancers[id]
	if !ok {
		return nil, fmt.Errorf("load balancer %q: %w", id, shared.ErrNotFound)
	}
	replaced := make(map[string]string, len(targets))
	for _, target := range targets {
		if target.Name == "" || target.IP == "" {
			return nil, fmt.Errorf("target name and target IP are required: %w", shared.ErrInvalidInput)
		}
		replaced[target.Name] = target.IP
	}
	entry.targets = replaced
	entry.lb.Port = port
	return fakeTargetPoolMap(entry), nil
}

func (c *Client) deleteLoadBalancer(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteLB); err != nil {
		return err
	}
	delete(c.loadBalancers, id)
	return nil
}

func fakeLoadBalancerMap(entry *lbEntry) map[string]any {
	m := map[string]any{
		"name":            entry.lb.Name,
		"externalAddress": entry.lb.IP,
		"listeners": []any{
			map[string]any{
				"displayName": "apiserver",
				"port":        entry.lb.Port,
				"targetPool":  "apiserver",
			},
		},
		"targetPools": []any{
			fakeTargetPoolMap(entry),
		},
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

func fakeTargetPoolMap(entry *lbEntry) map[string]any {
	targets := make([]any, 0, len(entry.targets))
	for name, ip := range entry.targets {
		targets = append(targets, map[string]any{
			"displayName": name,
			"ip":          ip,
		})
	}
	return map[string]any{
		"name":       "apiserver",
		"targetPort": entry.lb.Port,
		"targets":    targets,
	}
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
			err = c.addPublicIPToServer(r.Context(), serverID, publicIPID)
		case http.MethodDelete:
			err = c.removePublicIPFromServer(r.Context(), serverID, publicIPID)
		default:
			err = shared.ErrInvalidInput
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
		got, err := c.getPublicIP(r.Context(), id)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(fakePublicIPMap(got, nil))
		}
		return true

	case r.Method == http.MethodDelete && strings.Contains(path, "/public-ips/"):
		id := path[strings.LastIndex(path, "/public-ips/")+len("/public-ips/"):]
		writeFakeStatus(rec, c.deletePublicIP(r.Context(), id))
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
			err = shared.ErrInvalidInput
		}
		writeFakeStatus(rec, err)
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
		return nil, fmt.Errorf("empty tags: %w", shared.ErrInvalidInput)
	}
	items := make([]map[string]any, 0, len(c.publicIPs))
	for _, entry := range c.publicIPs {
		if shared.MapContains(entry.tags, tags) {
			items = append(items, fakePublicIPMap(entry.publicIP, entry.tags))
		}
	}
	return items, nil
}

func fakePublicIPMap(p *publicip.PublicIP, tags map[string]string) map[string]any {
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
		return fmt.Errorf("server %q: %w", serverID, shared.ErrNotFound)
	}
	if _, ok := c.securityGroups[securityGroupID]; !ok {
		return fmt.Errorf("security group %q: %w", securityGroupID, shared.ErrNotFound)
	}
	if serverEntry.securityGroups == nil {
		serverEntry.securityGroups = map[string]struct{}{}
	}
	serverEntry.securityGroups[securityGroupID] = struct{}{}
	c.AddSecurityGroupCalls++
	return nil
}

func (c *Client) handleServerRequest(rec *httptest.ResponseRecorder, r *http.Request) bool {
	path := r.URL.Path

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/servers"):
		tags := parseLabelSelector(r.URL.Query().Get("label_selector"))
		items, err := c.listServerItems(tags)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"items": items})
		}
		return true

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/servers"):
		c.handleCreateServer(rec, r)
		return true

	case r.Method == http.MethodGet && strings.Contains(path, "/servers/") && strings.HasSuffix(path, "/nics"):
		sub := path[strings.Index(path, "/servers/")+len("/servers/"):]
		serverID := strings.TrimSuffix(sub, "/nics")
		items, err := c.listServerNICItems(serverID)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(map[string]any{"items": items})
		}
		return true

	case r.Method == http.MethodGet && strings.Contains(path, "/servers/"):
		serverID := path[strings.LastIndex(path, "/servers/")+len("/servers/"):]
		item, err := c.getServerItem(serverID)
		if err != nil {
			writeFakeError(rec, err)
		} else {
			_ = json.NewEncoder(rec).Encode(item)
		}
		return true

	case r.Method == http.MethodDelete && strings.Contains(path, "/servers/"):
		serverID := path[strings.LastIndex(path, "/servers/")+len("/servers/"):]
		writeFakeStatus(rec, c.deleteServerHTTP(serverID))
		return true

	default:
		return false
	}
}

func (c *Client) deleteServerHTTP(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextDeleteServer); err != nil {
		return err
	}
	if _, ok := c.servers[id]; !ok {
		return fmt.Errorf("server %q: %w", id, shared.ErrNotFound)
	}
	delete(c.servers, id)
	return nil
}

func (c *Client) handleCreateServer(rec *httptest.ResponseRecorder, r *http.Request) {
	var body struct {
		Name             string            `json:"name"`
		MachineType      string            `json:"machineType"`
		ImageID          string            `json:"imageId"`
		AvailabilityZone string            `json:"availabilityZone"`
		KeypairName      string            `json:"keypairName"`
		SecurityGroups   []string          `json:"securityGroups"`
		UserData         string            `json:"userData"`
		Labels           map[string]string `json:"labels"`
		Networking       *struct {
			NetworkID string `json:"networkId"`
		} `json:"networking"`
		BootVolume *struct {
			Size                int    `json:"size"`
			PerformanceClass    string `json:"performanceClass"`
			DeleteOnTermination bool   `json:"deleteOnTermination"`
			Source              *struct {
				ID string `json:"id"`
			} `json:"source"`
		} `json:"bootVolume"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	var userData []byte
	if body.UserData != "" {
		userData, _ = base64.StdEncoding.DecodeString(body.UserData)
	}
	var networkID string
	if body.Networking != nil {
		networkID = body.Networking.NetworkID
	}
	imageID := body.ImageID
	var rootVolume server.RootVolume
	if body.BootVolume != nil {
		rootVolume = server.RootVolume{
			SizeGiB:             body.BootVolume.Size,
			PerformanceClass:    body.BootVolume.PerformanceClass,
			DeleteOnTermination: body.BootVolume.DeleteOnTermination,
		}
		if imageID == "" && body.BootVolume.Source != nil {
			imageID = body.BootVolume.Source.ID
		}
	}
	spec := server.ServerSpec{
		Name:             body.Name,
		MachineType:      body.MachineType,
		ImageID:          imageID,
		AvailabilityZone: body.AvailabilityZone,
		SSHKeyName:       body.KeypairName,
		NetworkID:        networkID,
		SecurityGroups:   body.SecurityGroups,
		UserData:         userData,
		Tags:             body.Labels,
		RootVolume:       rootVolume,
	}

	c.mu.Lock()
	if c.BeforeCreateServer != nil {
		c.mu.Unlock()
		c.BeforeCreateServer()
		c.mu.Lock()
	}
	if err := consume(&c.FailNextCreateServer); err != nil {
		c.mu.Unlock()
		writeFakeError(rec, err)
		return
	}
	entry := c.createServerEntry(spec)
	item := fakeServerMap(entry)
	c.mu.Unlock()

	_ = json.NewEncoder(rec).Encode(item)
}

func (c *Client) getServerItem(id string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextGetServer); err != nil {
		return nil, err
	}
	entry, ok := c.servers[id]
	if !ok {
		return nil, fmt.Errorf("server %q: %w", id, shared.ErrNotFound)
	}
	return fakeServerMap(entry), nil
}

func (c *Client) listServerNICItems(serverID string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.servers[serverID]
	if !ok {
		return nil, fmt.Errorf("server %q: %w", serverID, shared.ErrNotFound)
	}
	items := make([]map[string]any, 0, len(entry.server.Addresses))
	for _, addr := range entry.server.Addresses {
		items = append(items, map[string]any{"ipv4": addr.Address})
	}
	return items, nil
}

func (c *Client) listServerItems(tags map[string]string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := consume(&c.FailNextFindServer); err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(c.servers))
	for _, entry := range c.servers {
		if len(tags) > 0 && !shared.MapContains(entry.tags, tags) {
			continue
		}
		items = append(items, fakeServerMap(entry))
	}
	return items, nil
}

func fakeServerMap(entry *serverEntry) map[string]any {
	m := map[string]any{
		"id":               entry.server.ID,
		"name":             entry.server.Name,
		"machineType":      "c2i.1",
		"status":           entry.server.State,
		"availabilityZone": entry.server.AvailabilityZone,
	}
	if entry.server.PowerStatus != "" {
		m["powerStatus"] = entry.server.PowerStatus
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

func (c *Client) removeSecurityGroupFromServer(serverID, securityGroupID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	serverEntry, ok := c.servers[serverID]
	if !ok {
		return fmt.Errorf("server %q: %w", serverID, shared.ErrNotFound)
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
		return nil, fmt.Errorf("security group name is empty: %w", shared.ErrInvalidInput)
	}
	id := c.genID()
	sg := &securitygroup.SecurityGroup{
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
		return nil, fmt.Errorf("empty tags: %w", shared.ErrInvalidInput)
	}
	items := make([]map[string]any, 0, len(c.securityGroups))
	for _, entry := range c.securityGroups {
		if shared.MapContains(entry.tags, tags) {
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
		return nil, fmt.Errorf("security group %q: %w", sgID, shared.ErrNotFound)
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
		return nil, fmt.Errorf("security group %q: %w", sgID, shared.ErrNotFound)
	}
	if entry.rules == nil {
		entry.rules = make(map[string]*securityGroupRuleEntry)
	}
	rule.id = c.genID()
	stored := rule
	entry.rules[rule.id] = &stored
	return fakeSecurityGroupRuleMap(&stored), nil
}

func (c *Client) deleteSecurityGroupRule(sgID, ruleID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.securityGroups[sgID]
	if !ok {
		return fmt.Errorf("security group %q: %w", sgID, shared.ErrNotFound)
	}
	if entry.rules == nil {
		return fmt.Errorf("rule %q: %w", ruleID, shared.ErrNotFound)
	}
	if _, ok := entry.rules[ruleID]; !ok {
		return fmt.Errorf("rule %q: %w", ruleID, shared.ErrNotFound)
	}
	delete(entry.rules, ruleID)
	return nil
}

func (c *Client) deleteSecurityGroup(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if id == "" {
		return fmt.Errorf("security group ID is empty: %w", shared.ErrInvalidInput)
	}
	if _, ok := c.securityGroups[id]; !ok {
		return fmt.Errorf("security group %q: %w", id, shared.ErrNotFound)
	}
	for serverID, srv := range c.servers {
		if _, attached := srv.securityGroups[id]; attached {
			return fmt.Errorf("security group %q is still attached to server %q: %w", id, serverID, shared.ErrConflict)
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
	case shared.IsNotFound(err):
		status = http.StatusNotFound
	case shared.IsConflict(err):
		status = http.StatusConflict
	case shared.IsInvalidInput(err):
		status = http.StatusBadRequest
	case shared.IsUnauthorized(err):
		status = http.StatusUnauthorized
	}
	rec.WriteHeader(status)
	_ = json.NewEncoder(rec).Encode(map[string]any{"message": err.Error()})
}
