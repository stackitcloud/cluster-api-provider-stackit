/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"context"
	"errors"
	"testing"
)

const (
	testSDKProjectID = "00000000-0000-0000-0000-000000000000"
	testSDKRegion    = "eu01"
	testBoolTrue     = "true"
)

func TestNewClientValidation(t *testing.T) {
	t.Run("rejects empty project ID", func(t *testing.T) {
		_, err := NewClient(context.Background(), Credentials{
			Region:             testSDKRegion,
			ServiceAccountJSON: []byte(`{}`),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("NewClient() error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("rejects empty region", func(t *testing.T) {
		_, err := NewClient(context.Background(), Credentials{
			ProjectID:          testSDKProjectID,
			ServiceAccountJSON: []byte(`{}`),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("NewClient() error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("rejects empty service account JSON when STACKIT_NO_AUTH is not true", func(t *testing.T) {
		t.Setenv(stackitNoAuthEnv, "false")
		_, err := NewClient(context.Background(), Credentials{
			ProjectID: testSDKProjectID,
			Region:    testSDKRegion,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("NewClient() error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("creates SDKClient when STACKIT_NO_AUTH is true", func(t *testing.T) {
		t.Setenv(stackitNoAuthEnv, testBoolTrue)
		client, err := NewClient(context.Background(), Credentials{
			ProjectID: testSDKProjectID,
			Region:    testSDKRegion,
		})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		if client.ProjectID() != testSDKProjectID || client.Region() != testSDKRegion {
			t.Fatalf("client ProjectID=%q Region=%q", client.ProjectID(), client.Region())
		}
		if client.IaaSClient() == nil || client.LoadBalancerClient() == nil {
			t.Fatal("expected non-nil IaaSClient and LoadBalancerClient")
		}
	})
}
