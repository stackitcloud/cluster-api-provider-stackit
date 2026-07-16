/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

const (
	envProjectID          = "STACKIT_PROJECT_ID"
	envRegion             = "STACKIT_REGION"
	envServiceAccountJSON = "STACKIT_SERVICE_ACCOUNT_JSON"
	envServiceAccountFile = "STACKIT_SERVICE_ACCOUNT_JSON_FILE"
	envCleanupTestID      = "STACKIT_E2E_TEST_ID"
)

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cleanup-stackit: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	testID, err := requiredEnv(envCleanupTestID)
	if err != nil {
		return err
	}
	projectID, err := requiredEnv(envProjectID)
	if err != nil {
		return err
	}
	region := envDefault(envRegion, "eu01")
	serviceAccountJSON, err := serviceAccountJSON()
	if err != nil {
		return err
	}

	client, err := cloud.NewClient(ctx, cloud.Credentials{
		ProjectID:          projectID,
		Region:             region,
		ServiceAccountJSON: serviceAccountJSON,
	})
	if err != nil {
		return err
	}

	tags := map[string]string{
		util.LabelE2E:    util.E2EValue,
		util.LabelTestID: testID,
	}
	return cloud.CleanupByTags(ctx, client, tags)
}

func serviceAccountJSON() ([]byte, error) {
	if value := os.Getenv(envServiceAccountJSON); value != "" {
		return []byte(value), nil
	}
	path := os.Getenv(envServiceAccountFile)
	if path == "" {
		return nil, fmt.Errorf("set %s or %s", envServiceAccountFile, envServiceAccountJSON)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s or set %s: %w", envServiceAccountFile, envServiceAccountJSON, err)
	}
	return data, nil
}

func requiredEnv(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
