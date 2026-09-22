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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestConfigureNamespaces(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		namespace               string
		leaderElectionNamespace string
		wantLeaseNamespace      string
		wantError               string
	}{
		{name: "cluster wide defaults"},
		{name: "namespaced lease defaults", namespace: "clusters-example", wantLeaseNamespace: "clusters-example"},
		{
			name: "explicit lease namespace", namespace: "clusters-example",
			leaderElectionNamespace: "provider-system", wantLeaseNamespace: "provider-system",
		},
		{
			name:                    "cluster wide with explicit lease namespace",
			leaderElectionNamespace: "provider-system", wantLeaseNamespace: "provider-system",
		},
		{name: "reject multiple namespaces", namespace: "one,two", wantError: "--namespace"},
		{name: "reject whitespace", namespace: " ", wantError: "--namespace"},
		{name: "reject uppercase", namespace: "HostedCluster", wantError: "--namespace"},
		{name: "reject invalid lease namespace", leaderElectionNamespace: "one/two", wantError: "--leader-election-namespace"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := ctrl.Options{}
			err := configureNamespaces(&options, tt.namespace, tt.leaderElectionNamespace)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("configureNamespaces() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("configureNamespaces() error = %v", err)
			}
			if options.LeaderElectionNamespace != tt.wantLeaseNamespace {
				t.Fatalf("lease namespace = %q, want %q", options.LeaderElectionNamespace, tt.wantLeaseNamespace)
			}
			if tt.namespace == "" {
				if options.Cache.DefaultNamespaces != nil {
					t.Fatal("cluster-wide manager unexpectedly restricts namespaces")
				}
			} else if _, ok := options.Cache.DefaultNamespaces[tt.namespace]; !ok || len(options.Cache.DefaultNamespaces) != 1 {
				t.Fatalf("cache namespaces = %v, want only %q", options.Cache.DefaultNamespaces, tt.namespace)
			}
		})
	}
}

// Exercise the actual controller-runtime cache: a namespaced deployment must
// neither observe nor read another hosted cluster's bootstrap or cloud secrets.
func TestNamespaceCacheIsolation(t *testing.T) {
	testEnv := &envtest.Environment{}
	config, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	liveClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create live client: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, namespace := range []string{"hosted-one", "hosted-two"} {
		if err := liveClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
			t.Fatalf("create namespace: %v", err)
		}
		if err := liveClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "credentials"},
			Data:       map[string][]byte{"value": []byte(namespace)},
		}); err != nil {
			t.Fatalf("create secret: %v", err)
		}
	}
	options := ctrl.Options{}
	if err := configureNamespaces(&options, "hosted-one", ""); err != nil {
		t.Fatal(err)
	}
	options.Cache.Scheme = scheme
	scopedCache, err := cache.New(config, options.Cache)
	if err != nil {
		t.Fatalf("create namespace cache: %v", err)
	}
	cacheErrors := make(chan error, 1)
	go func() { cacheErrors <- scopedCache.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-cacheErrors; err != nil {
			t.Errorf("run cache: %v", err)
		}
	})
	secret := &corev1.Secret{}
	if err := scopedCache.Get(ctx, client.ObjectKey{Namespace: "hosted-one", Name: "credentials"}, secret); err != nil {
		t.Fatalf("get watched secret: %v", err)
	}
	if string(secret.Data["value"]) != "hosted-one" {
		t.Fatalf("read wrong namespace's secret: %s", secret.Namespace)
	}
	if err := scopedCache.Get(ctx, client.ObjectKey{Namespace: "hosted-two", Name: "credentials"}, &corev1.Secret{}); err == nil {
		t.Fatal("cache permitted a secret read outside its namespace")
	}
	secrets := &corev1.SecretList{}
	if err := scopedCache.List(ctx, secrets); err != nil {
		t.Fatalf("list watched secrets: %v", err)
	}
	if len(secrets.Items) != 1 || secrets.Items[0].Namespace != "hosted-one" {
		t.Fatalf("cache list escaped namespace scope: %v", secrets.Items)
	}
}
