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
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseCredentialsSecret(t *testing.T) {
	secret := credentialsSecret("project-id")

	got, err := ParseCredentialsSecret(secret, "project-id", "eu01")
	if err != nil {
		t.Fatalf("ParseCredentialsSecret() error = %v", err)
	}
	if got.ProjectID != "project-id" || got.Region != "eu01" || string(got.ServiceAccountJSON) != "{}" {
		t.Fatalf("ParseCredentialsSecret() = %#v", got)
	}
}

func TestParseCredentialsSecretUsesSecretProjectID(t *testing.T) {
	got, err := ParseCredentialsSecret(credentialsSecret("project-id"), "", "eu01")
	if err != nil {
		t.Fatalf("ParseCredentialsSecret() error = %v", err)
	}
	if got.ProjectID != "project-id" {
		t.Fatalf("ProjectID = %q, want project-id", got.ProjectID)
	}
}

func TestParseCredentialsSecretProjectIDConflict(t *testing.T) {
	_, err := ParseCredentialsSecret(credentialsSecret("secret-project-id"), "spec-project-id", "eu01")
	if !errors.Is(err, ErrCredentialsInvalid) {
		t.Fatalf("ParseCredentialsSecret() error = %v, want ErrCredentialsInvalid", err)
	}
}

func TestParseCredentialsSecretMissingServiceAccount(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"project-id": []byte("project-id"),
	}}

	_, err := ParseCredentialsSecret(secret, "project-id", "eu01")
	if !errors.Is(err, ErrCredentialsInvalid) {
		t.Fatalf("ParseCredentialsSecret() error = %v, want ErrCredentialsInvalid", err)
	}
}

func credentialsSecret(projectID string) *corev1.Secret {
	return &corev1.Secret{Data: map[string][]byte{
		"project-id":          []byte(projectID),
		"serviceaccount.json": []byte("{}"),
	}}
}
