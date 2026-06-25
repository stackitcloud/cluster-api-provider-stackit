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

func TestExtractBootstrapDataPrefersValue(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"value":    []byte("value-data"),
		"userData": []byte("userdata-data"),
	}}

	got, err := ExtractBootstrapData(secret)
	if err != nil {
		t.Fatalf("ExtractBootstrapData() error = %v", err)
	}
	if string(got) != "value-data" {
		t.Fatalf("ExtractBootstrapData() = %q, want value-data", got)
	}
}

func TestExtractBootstrapDataFallsBackToUserData(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"userData": []byte("userdata-data"),
	}}

	got, err := ExtractBootstrapData(secret)
	if err != nil {
		t.Fatalf("ExtractBootstrapData() error = %v", err)
	}
	if string(got) != "userdata-data" {
		t.Fatalf("ExtractBootstrapData() = %q, want userdata-data", got)
	}
}

func TestExtractBootstrapDataInvalid(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"other": []byte("data"),
	}}

	_, err := ExtractBootstrapData(secret)
	if !errors.Is(err, ErrBootstrapDataInvalid) {
		t.Fatalf("ExtractBootstrapData() error = %v, want ErrBootstrapDataInvalid", err)
	}
}
