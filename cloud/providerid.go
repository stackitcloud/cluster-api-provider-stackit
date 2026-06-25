/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"errors"
	"fmt"
	"strings"
)

// providerIDScheme is the URL scheme used for STACKIT providerIDs.
const providerIDScheme = "stackit://"

// ErrInvalidProviderID is returned by ParseProviderID for malformed values.
var ErrInvalidProviderID = errors.New("invalid providerID")

// NewProviderID returns a providerID string in the STACKIT format used by
// cloud-provider-stackit.
//
// Format verified against the local cloud-provider-stackit repository:
//   - pkg/ccm/instances.go: Instances.makeInstanceID returns stackit://<server-id>
//   - pkg/ccm/instances.go: instanceIDFromProviderID parses only the server ID
//
// Project ID and region are intentionally not encoded in the providerID. The
// cloud-provider-stackit controller gets project and region from its own
// configuration and then resolves Node.spec.providerID via GetServer(projectID,
// region, serverID).
func NewProviderID(projectID, region, serverID string) string {
	return fmt.Sprintf("%s%s", providerIDScheme, serverID)
}

// ParseProviderID splits a providerID string into its components. The current
// STACKIT providerID format does not contain project or region, so those return
// values are empty. It returns ErrInvalidProviderID if any encoded component is
// empty or the scheme is wrong.
func ParseProviderID(providerID string) (projectID, region, serverID string, err error) {
	if !strings.HasPrefix(providerID, providerIDScheme) {
		return "", "", "", fmt.Errorf("%w: missing scheme %q", ErrInvalidProviderID, providerIDScheme)
	}
	serverID = strings.TrimPrefix(providerID, providerIDScheme)
	if serverID == "" || strings.Contains(serverID, "/") {
		return "", "", "", fmt.Errorf("%w: expected <server>, got %q", ErrInvalidProviderID, serverID)
	}
	return "", "", serverID, nil
}
