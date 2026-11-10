/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shared

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stackitcloud/stackit-sdk-go/core/oapierror"
)

func MapContains(haystack, needle map[string]string) bool {
	for k, v := range needle {
		if haystack[k] != v {
			return false
		}
	}
	return true
}

// TODO: Move to LB Service
func LoadBalancerLabels(tags map[string]string) map[string]string {
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		out[strings.ReplaceAll(k, "/", ".")] = v
	}
	return out
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
