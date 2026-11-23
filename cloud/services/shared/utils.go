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
	"time"

	"github.com/stackitcloud/stackit-sdk-go/core/oapierror"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer/v2api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	// RetryableErrorRequeueAfter paces retries of transient or conflicting cloud API operations.
	RetryableErrorRequeueAfter = 5 * time.Second

	// DeleteRequeueAfter paces the wait for dependent resources to disappear during deletion.
	DeleteRequeueAfter = 5 * time.Second
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

// ReconcileScope provides condition, event, and failure result helpers for cloud resource reconcilers.
type ReconcileScope interface {
	Name() string
	SetConditions(status metav1.ConditionStatus, reason, message string, conditionTypes ...string)
	SetNotReady(reason, message string, conditionTypes ...string)
	CloudFailureResult(reason string, err error, requeueAfter time.Duration, returnError bool, conditionTypes ...string) (ctrl.Result, error)
	FindCondition(conditionType string) *metav1.Condition
	Event(reason, message string)
	Eventf(reason, messageFmt string, args ...any)
	Warn(reason, message string)
	Warnf(reason, messageFmt string, args ...any)
}

// NoOpReconcileScope provides default no-op implementations of ReconcileScope
// for headless callers (such as tag cleanup) and service unit tests.
type NoOpReconcileScope struct{}

func (NoOpReconcileScope) Name() string { return "" }

func (NoOpReconcileScope) SetConditions(_ metav1.ConditionStatus, _, _ string, _ ...string) {}

func (NoOpReconcileScope) SetNotReady(_, _ string, _ ...string) {}

func (NoOpReconcileScope) CloudFailureResult(_ string, err error, _ time.Duration, _ bool, _ ...string) (ctrl.Result, error) {
	return ctrl.Result{}, err
}

func (NoOpReconcileScope) FindCondition(_ string) *metav1.Condition { return nil }

func (NoOpReconcileScope) Event(_, _ string) {}

func (NoOpReconcileScope) Eventf(_, _ string, _ ...any) {}

func (NoOpReconcileScope) Warn(_, _ string) {}

func (NoOpReconcileScope) Warnf(_, _ string, _ ...any) {}

func (NoOpReconcileScope) ServerGoneResult(err error) (ctrl.Result, error) {
	return ctrl.Result{}, err
}

// ListByTags validates that tags are non-empty, lists SDK items using the tag label selector,
// filters items whose labels contain all tags, and maps each matching item via convert.
func ListByTags[T any, Out any](
	tags map[string]string,
	list func(labelSelector string) ([]T, error),
	convert func(*T) Out,
) ([]Out, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: empty tag selector", ErrInvalidInput)
	}
	items, err := list(labelSelector(tags))
	if err != nil {
		return nil, err
	}
	matched := []Out{}
	for _, item := range items {
		if itemContainsTags(&item, tags) {
			matched = append(matched, convert(&item))
		}
	}
	return matched, nil
}

// ListServersByTags lists STACKIT servers matching the given tag selector.
func ListServersByTags(ctx context.Context, scope IaaSClientScope, tags map[string]string) ([]*iaas.Server, error) {
	return ListByTags(tags, func(selector string) ([]iaas.Server, error) {
		resp, err := scope.IaaSClient().DefaultAPI.ListServers(ctx, scope.ProjectID(), scope.Region()).
			LabelSelector(selector).
			Details(true).
			Execute()
		if err != nil {
			return nil, ClassifySDKError("list servers", err)
		}
		return resp.GetItems(), nil
	}, func(srv *iaas.Server) *iaas.Server {
		return srv
	})
}

// ListServerIDsByTags lists the IDs of STACKIT servers matching the given tag selector.
func ListServerIDsByTags(ctx context.Context, scope IaaSClientScope, tags map[string]string) ([]string, error) {
	return ListIDsByTags(ctx, tags, func(ctx context.Context, tags map[string]string) ([]*iaas.Server, error) {
		return ListServersByTags(ctx, scope, tags)
	}, (*iaas.Server).GetId)
}

// ListIDsByTags lists resources by tags and collects their non-empty IDs.
func ListIDsByTags[T any](
	ctx context.Context,
	tags map[string]string,
	listByTags func(context.Context, map[string]string) ([]T, error),
	getID func(T) string,
) ([]string, error) {
	items, err := listByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := getID(item); id != "" {
			ids = append(ids, id)
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
	if len(matched) == 0 {
		return zero, fmt.Errorf("%w: no %s matching tags", ErrNotFound, singular)
	}
	if len(matched) > 1 {
		return zero, fmt.Errorf("%w: multiple %s matching tags", ErrConflict, plural)
	}
	return matched[0], nil
}

// ResolveExisting looks up an existing resource by ID first (ignoring errors matched by ignoreLookupErr),
// falling back to tag lookup when the ID is empty or absent. It returns the zero value and nil error when not found.
func ResolveExisting[T any](
	ctx context.Context,
	id string,
	tags map[string]string,
	getByID func(context.Context, string) (T, error),
	findByTags func(context.Context, map[string]string) (T, error),
	ignoreLookupErr func(error) bool,
) (T, error) {
	var zero T
	if id != "" {
		item, err := getByID(ctx, id)
		if err == nil {
			return item, nil
		}
		if !ignoreLookupErr(err) {
			return zero, err
		}
	}
	if len(tags) == 0 {
		return zero, nil
	}
	item, err := findByTags(ctx, tags)
	if err == nil {
		return item, nil
	}
	if ignoreLookupErr(err) {
		return zero, nil
	}
	return zero, err
}

// DeleteByIDOrTags deletes a resource by ID when provided, falling back to deleting all resources matching tags
// if the ID is empty or already absent.
func DeleteByIDOrTags(
	ctx context.Context,
	id string,
	tags map[string]string,
	deleteByID func(context.Context, string) (bool, error),
	listIDsByTags func(context.Context, map[string]string) ([]string, error),
) ([]string, error) {
	if id != "" {
		deleted, err := deleteByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if deleted {
			return []string{id}, nil
		}
	}
	if len(tags) == 0 {
		return nil, nil
	}
	ids, err := listIDsByTags(ctx, tags)
	if err != nil {
		return nil, err
	}
	var (
		deletedIDs []string
		errs       []error
	)
	for _, targetID := range ids {
		if targetID == "" {
			continue
		}
		deleted, err := deleteByID(ctx, targetID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if deleted {
			deletedIDs = append(deletedIDs, targetID)
		}
	}
	return deletedIDs, errors.Join(errs...)
}

func itemContainsTags(item any, tags map[string]string) bool {
	switch labeled := item.(type) {
	case interface{ GetLabels() map[string]any }:
		return MapContains(labeled.GetLabels(), tags)
	case interface{ GetLabels() map[string]string }:
		return MapContains(labeled.GetLabels(), tags)
	default:
		return false
	}
}

func MapContains[V any](haystack map[string]V, needle map[string]string) bool {
	for k, want := range needle {
		got, ok := haystack[k]
		if !ok || fmt.Sprint(got) != want {
			return false
		}
	}
	return true
}

func labelSelector(tags map[string]string) string {
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
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
