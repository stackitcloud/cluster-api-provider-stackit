/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import "errors"

// Sentinel errors returned by Client implementations. Controllers use
// errors.Is to classify them into requeue / condition-update behavior.
var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrInvalidInput = errors.New("invalid input")
	ErrConflict     = errors.New("conflict")
	ErrTransient    = errors.New("transient")
)

// IsNotFound reports whether err wraps ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsUnauthorized reports whether err wraps ErrUnauthorized.
func IsUnauthorized(err error) bool { return errors.Is(err, ErrUnauthorized) }

// IsInvalidInput reports whether err wraps ErrInvalidInput.
func IsInvalidInput(err error) bool { return errors.Is(err, ErrInvalidInput) }

// IsConflict reports whether err wraps ErrConflict.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }

// IsTransient reports whether err wraps ErrTransient.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }

// IsRetryable reports whether err should trigger a requeue (conflict or
// transient).
func IsRetryable(err error) bool {
	return IsConflict(err) || IsTransient(err)
}
