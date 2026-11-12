/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shared

import (
	"fmt"
	"testing"
)

func TestErrorClassification(t *testing.T) {
	if !IsNotFound(fmt.Errorf("wrapped: %w", ErrNotFound)) {
		t.Fatal("IsNotFound() = false, want true")
	}
	if !IsUnauthorized(fmt.Errorf("wrapped: %w", ErrUnauthorized)) {
		t.Fatal("IsUnauthorized() = false, want true")
	}
	if !IsInvalidInput(fmt.Errorf("wrapped: %w", ErrInvalidInput)) {
		t.Fatal("IsInvalidInput() = false, want true")
	}
	if !IsConflict(fmt.Errorf("wrapped: %w", ErrConflict)) {
		t.Fatal("IsConflict() = false, want true")
	}
	if !IsTransient(fmt.Errorf("wrapped: %w", ErrTransient)) {
		t.Fatal("IsTransient() = false, want true")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "conflict", err: ErrConflict, want: true},
		{name: "transient", err: ErrTransient, want: true},
		{name: "not found", err: ErrNotFound, want: false},
		{name: "unauthorized", err: ErrUnauthorized, want: false},
		{name: "invalid input", err: ErrInvalidInput, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Fatalf("IsRetryable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestFindSingle(t *testing.T) {
	t.Run("returns ErrNotFound when empty", func(t *testing.T) {
		_, err := FindSingle([]string{}, "server", "servers")
		if !IsNotFound(err) {
			t.Fatalf("FindSingle() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("returns ErrConflict when multiple", func(t *testing.T) {
		_, err := FindSingle([]string{"a", "b"}, "server", "servers")
		if !IsConflict(err) {
			t.Fatalf("FindSingle() error = %v, want ErrConflict", err)
		}
	})

	t.Run("returns single element", func(t *testing.T) {
		got, err := FindSingle([]string{"a"}, "server", "servers")
		if err != nil {
			t.Fatalf("FindSingle() error = %v", err)
		}
		if got != "a" {
			t.Fatalf("FindSingle() = %q, want %q", got, "a")
		}
	})
}
