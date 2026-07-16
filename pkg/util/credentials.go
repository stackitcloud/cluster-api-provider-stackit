/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package util

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
)

// Keys expected in the credentials Secret. The format mirrors the existing
// STACKIT MCM provider; see spec section 12.
const (
	credentialsKeyProjectID   = "project-id"
	credentialsKeyServiceAcct = "serviceaccount.json"
)

// ErrCredentialsInvalid is returned when the Secret is missing required keys
// or contains a conflicting project ID.
var ErrCredentialsInvalid = errors.New("credentials invalid")

// ParseCredentialsSecret extracts STACKIT credentials from a Secret.
//
// If specProjectID is non-empty it must match the project-id stored in the
// Secret (if any); otherwise the function returns ErrCredentialsInvalid.
//
// If specProjectID is empty, the function falls back to the value from the
// Secret. region is taken straight from the spec; the Secret does not carry
// a region.
func ParseCredentialsSecret(secret *corev1.Secret, specProjectID, region string) (cloud.Credentials, error) {
	sa, ok := secret.Data[credentialsKeyServiceAcct]
	if !ok || len(sa) == 0 {
		return cloud.Credentials{}, fmt.Errorf("%w: missing %q key", ErrCredentialsInvalid, credentialsKeyServiceAcct)
	}

	secretProjectID := string(secret.Data[credentialsKeyProjectID])
	projectID := specProjectID
	switch {
	case specProjectID != "" && secretProjectID != "" && specProjectID != secretProjectID:
		return cloud.Credentials{}, fmt.Errorf("%w: spec.projectID %q does not match secret project-id %q",
			ErrCredentialsInvalid, specProjectID, secretProjectID)
	case projectID == "":
		projectID = secretProjectID
	}
	if projectID == "" {
		return cloud.Credentials{}, fmt.Errorf("%w: no project ID in spec or secret", ErrCredentialsInvalid)
	}

	return cloud.Credentials{
		ProjectID:          projectID,
		Region:             region,
		ServiceAccountJSON: sa,
	}, nil
}
