/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

type cleanupScope struct {
	Client
	tags map[string]string
}

func (s *cleanupScope) PublicIPSpec() publicip.PublicIPSpec {
	return publicip.PublicIPSpec{
		Tags:           s.tags,
		ServerSelector: s.tags,
	}
}

func (s *cleanupScope) LoadBalancerSpec() loadbalancer.LoadBalancerSpec {
	return loadbalancer.LoadBalancerSpec{
		Tags: s.tags,
	}
}

func (s *cleanupScope) ServerSpec() server.ServerSpec {
	return server.ServerSpec{
		Tags: s.tags,
	}
}

func (s *cleanupScope) SecurityGroupSpecs() []securitygroup.SecurityGroupSpec {
	return []securitygroup.SecurityGroupSpec{
		{
			Tags:           s.tags,
			ServerSelector: s.tags,
		},
	}
}

// CleanupByTags deletes provider-managed cloud resources matching tags without
// relying on Kubernetes objects. Load balancers are deleted before servers so
// server deletion is not blocked by target attachments.
func CleanupByTags(ctx context.Context, client Client, tags map[string]string) error {
	if len(tags) == 0 {
		return fmt.Errorf("%w: empty cleanup tag selector", shared.ErrInvalidInput)
	}

	scope := &cleanupScope{
		Client: client,
		tags:   tags,
	}
	var errs []string
	for _, deleteFn := range []func(context.Context) error{
		publicip.New(scope).Delete,
		loadbalancer.New(scope).Delete,
		securitygroup.New(scope).Delete,
		server.New(scope).Delete,
	} {
		if err := deleteFn(ctx); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", shared.ErrTransient, strings.Join(errs, "; "))
	}
	return nil
}
