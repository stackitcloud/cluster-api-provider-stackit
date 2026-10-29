/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"

// Sentinel errors returned by Client implementations, re-exported from shared.
// Controllers use errors.Is to classify them into requeue / condition-update behavior.
var (
	ErrNotFound     = shared.ErrNotFound
	ErrUnauthorized = shared.ErrUnauthorized
	ErrInvalidInput = shared.ErrInvalidInput
	ErrConflict     = shared.ErrConflict
	ErrTransient    = shared.ErrTransient
)

// IsNotFound reports whether err wraps ErrNotFound.
func IsNotFound(err error) bool { return shared.IsNotFound(err) }

// IsUnauthorized reports whether err wraps ErrUnauthorized.
func IsUnauthorized(err error) bool { return shared.IsUnauthorized(err) }

// IsInvalidInput reports whether err wraps ErrInvalidInput.
func IsInvalidInput(err error) bool { return shared.IsInvalidInput(err) }

// IsConflict reports whether err wraps ErrConflict.
func IsConflict(err error) bool { return shared.IsConflict(err) }

// IsTransient reports whether err wraps ErrTransient.
func IsTransient(err error) bool { return shared.IsTransient(err) }

// IsRetryable reports whether err should trigger a requeue (conflict or transient).
func IsRetryable(err error) bool { return shared.IsRetryable(err) }
