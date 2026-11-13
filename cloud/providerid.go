/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"fmt"
)

// providerIDScheme is the URL scheme used for STACKIT providerIDs.
const providerIDScheme = "stackit://"

// NewProviderID returns a providerID string in the STACKIT format used by
// cloud-provider-stackit.
//
// Format verified against the local cloud-provider-stackit repository:
//   - pkg/ccm/instances.go: Instances.makeInstanceID returns stackit://<server-id>
//
// Project ID and region are intentionally not encoded in the providerID. The
// cloud-provider-stackit controller gets project and region from its own
// configuration and then resolves Node.spec.providerID via GetServer(projectID,
// region, serverID).
func NewProviderID(serverID string) string {
	return fmt.Sprintf("%s%s", providerIDScheme, serverID)
}
