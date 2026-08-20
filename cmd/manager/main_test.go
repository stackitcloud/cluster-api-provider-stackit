/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// secretCacheConfig returns the cache configuration registered for Secrets.
// ByObject is keyed by a sample object rather than by type, so the entry has to
// be found by the key's type — a freshly allocated &corev1.Secret{} is a
// different pointer and would never match as a map key.
func secretCacheConfig(t *testing.T) cache.ByObject {
	t.Helper()

	for obj, byObject := range managerCacheOptions().ByObject {
		if _, ok := obj.(*corev1.Secret); ok {
			return byObject
		}
	}
	t.Fatal("no cache configuration registered for Secrets")
	return cache.ByObject{}
}

func TestManagerCacheOptionsStripsSecretData(t *testing.T) {
	transform := secretCacheConfig(t).Transform
	if transform == nil {
		t.Fatal("Secret cache configuration has no Transform")
	}

	out, err := transform(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationUpdate}},
		},
		Data: map[string][]byte{"credentials": []byte("service-account-key")},
	})
	if err != nil {
		t.Fatalf("Transform returned an error: %v", err)
	}
	secret, ok := out.(*corev1.Secret)
	if !ok {
		t.Fatalf("Transform returned %T, want *corev1.Secret", out)
	}
	if secret.Data != nil {
		t.Errorf("Transform left Secret data in the cache: %v", secret.Data)
	}
	if secret.ManagedFields != nil {
		t.Errorf("Transform left managed fields in the cache: %v", secret.ManagedFields)
	}
}

// The Transform runs on everything committed to the Secret informer, including
// the tombstones the cache emits on deletion, so it has to pass anything that
// is not a Secret straight through instead of dropping it.
func TestManagerCacheOptionsPassesThroughNonSecrets(t *testing.T) {
	in := &corev1.ConfigMap{Data: map[string]string{"key": "value"}}

	out, err := secretCacheConfig(t).Transform(in)
	if err != nil {
		t.Fatalf("Transform returned an error: %v", err)
	}
	if out != any(in) {
		t.Errorf("Transform returned %v, want the input unchanged", out)
	}
}

func TestManagerClientOptionsDisablesSecretCache(t *testing.T) {
	cacheOptions := managerClientOptions().Cache
	if cacheOptions == nil {
		t.Fatal("client options have no cache configuration")
	}
	for _, obj := range cacheOptions.DisableFor {
		if _, ok := obj.(*corev1.Secret); ok {
			return
		}
	}
	t.Errorf("Secrets are not in DisableFor: %v", cacheOptions.DisableFor)
}
