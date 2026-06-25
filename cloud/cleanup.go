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
	"maps"
	"strings"
)

// CleanupByTags deletes provider-managed cloud resources matching tags without
// relying on Kubernetes objects. Load balancers are deleted before servers so
// server deletion is not blocked by target attachments.
func CleanupByTags(ctx context.Context, client Client, tags map[string]string) error {
	if len(tags) == 0 {
		return fmt.Errorf("%w: empty cleanup tag selector", ErrInvalidInput)
	}

	var errs []string
	bastionTags := make(map[string]string, len(tags)+1)
	maps.Copy(bastionTags, tags)
	bastionTags["cluster-api-provider-stackit/resource-role"] = "bastion"
	if err := client.DeleteBastion(ctx, BastionInput{Tags: bastionTags}, Bastion{}); err != nil && !IsNotFound(err) {
		errs = append(errs, fmt.Sprintf("delete bastion resources: %v", err))
	}

	loadBalancers, err := client.ListAPIServerLoadBalancersByTags(ctx, tags)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		for _, loadBalancer := range loadBalancers {
			if err := client.DeleteAPIServerLoadBalancer(ctx, loadBalancer.ID); err != nil && !IsNotFound(err) {
				errs = append(errs, fmt.Sprintf("delete load balancer %q: %v", loadBalancer.ID, err))
			}
		}
	}

	servers, err := client.ListServersByTags(ctx, tags)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		for _, server := range servers {
			if err := client.DeleteServer(ctx, server.ID); err != nil && !IsNotFound(err) {
				errs = append(errs, fmt.Sprintf("delete server %q: %v", server.ID, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrTransient, strings.Join(errs, "; "))
	}
	return nil
}
