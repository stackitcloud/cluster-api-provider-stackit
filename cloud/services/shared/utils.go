/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shared

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stackitcloud/stackit-sdk-go/core/oapierror"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
)

// IaaSClientScope is an interface that provides the IaaS client and target scope for IaaS operations.
type IaaSClientScope interface {
	ProjectID() string
	Region() string
	IaaSClient() *iaas.APIClient
}

// LoadBalancerClientScope is an interface that provides the LoadBalancer client and target scope for load balancer operations.
type LoadBalancerClientScope interface {
	ProjectID() string
	Region() string
	LoadBalancerClient() *lb.APIClient
}

// ListServerIDsByTags lists the IDs of STACKIT servers matching the given tag selector.
func ListServerIDsByTags(ctx context.Context, scope IaaSClientScope, tags map[string]string) ([]string, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", ErrInvalidInput)
	}
	resp, err := scope.IaaSClient().DefaultAPI.ListServers(ctx, scope.ProjectID(), scope.Region()).
		LabelSelector(LabelSelector(tags)).
		Execute()
	if err != nil {
		return nil, ClassifySDKError("list servers", err)
	}
	var ids []string
	for _, srv := range resp.GetItems() {
		if LabelsContainTags(srv.GetLabels(), tags) {
			if id := srv.GetId(); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// FindByTags lists resources by tags and returns the single matching item, or ErrNotFound if empty, or ErrConflict if more than one.
func FindByTags[T any](
	ctx context.Context,
	tags map[string]string,
	listByTags func(context.Context, map[string]string) ([]T, error),
	singular, plural string,
) (T, error) {
	var zero T
	matched, err := listByTags(ctx, tags)
	if err != nil {
		return zero, err
	}
	return findSingle(matched, singular, plural)
}

// findSingle returns the single item in matched, or ErrNotFound if empty, or ErrConflict if more than one.
func findSingle[T any](matched []T, singular, plural string) (T, error) {
	var zero T
	if len(matched) == 0 {
		return zero, fmt.Errorf("%w: no %s matching tags", ErrNotFound, singular)
	}
	if len(matched) > 1 {
		return zero, fmt.Errorf("%w: multiple %s matching tags", ErrConflict, plural)
	}
	return matched[0], nil
}

func MapContains(haystack, needle map[string]string) bool {
	for k, v := range needle {
		if haystack[k] != v {
			return false
		}
	}
	return true
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func LabelSelector(tags map[string]string) string {
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func LabelsContainTags(labels map[string]any, tags map[string]string) bool {
	for k, want := range tags {
		got, ok := labels[k]
		if !ok || fmt.Sprint(got) != want {
			return false
		}
	}
	return true
}

func TagsToSDKLabels(tags map[string]string) map[string]any {
	out := make(map[string]any, len(tags))
	for k, v := range tags {
		out[k] = v
	}
	return out
}

func ClassifySDKError(op string, err error) error {
	var oapiErr *oapierror.GenericOpenAPIError
	if errors.As(err, &oapiErr) {
		switch oapiErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s: %v", ErrNotFound, op, err)
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %s: %v", ErrUnauthorized, op, err)
		case http.StatusConflict:
			return fmt.Errorf("%w: %s: %v", ErrConflict, op, err)
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %s: %v", ErrInvalidInput, op, err)
		case http.StatusTooManyRequests:
			return fmt.Errorf("%w: %s: %v", ErrTransient, op, err)
		default:
			if oapiErr.StatusCode >= 500 {
				return fmt.Errorf("%w: %s: %v", ErrTransient, op, err)
			}
		}
	}
	return fmt.Errorf("%w: %s: %v", ErrTransient, op, err)
}
