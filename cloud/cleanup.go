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

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/loadbalancer"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
)

// CleanupByTags deletes provider-managed cloud resources matching tags without
// relying on Kubernetes objects. Load balancers are deleted before servers so
// server deletion is not blocked by target attachments.
func CleanupByTags(ctx context.Context, client Client, tags map[string]string) error {
	if len(tags) == 0 {
		return fmt.Errorf("%w: empty cleanup tag selector", ErrInvalidInput)
	}

	var errs []string
	srvClient := server.NewClient(client)
	sgClient := securitygroup.NewClient(client)

	bastionTags := make(map[string]string, len(tags)+1)
	maps.Copy(bastionTags, tags)
	bastionTags["cluster-api-provider-stackit/resource-role"] = "bastion"
	bastionServers, err := srvClient.ListByTags(ctx, bastionTags)
	if err != nil {
		errs = append(errs, err.Error())
	}
	publicIPs, err := publicip.ListByTags(ctx, client.IaaSClient(), client.ProjectID(), client.Region(), tags)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		for _, pubIP := range publicIPs {
			for _, srv := range bastionServers {
				if err := publicip.DetachFromServer(ctx, client.IaaSClient(), client.ProjectID(), client.Region(), srv.ID, pubIP.ID); err != nil {
					errs = append(errs, fmt.Sprintf("detach public IP %q from server %q: %v", pubIP.ID, srv.ID, err))
				}
			}
			if err := publicip.DeleteByID(ctx, client.IaaSClient(), client.ProjectID(), client.Region(), pubIP.ID); err != nil {
				errs = append(errs, fmt.Sprintf("delete public IP %q: %v", pubIP.ID, err))
			}
		}
	}

	lbClient := loadbalancer.NewClient(client)
	loadBalancers, err := lbClient.ListByTags(ctx, tags)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		for _, lb := range loadBalancers {
			if err := lbClient.DeleteByID(ctx, lb.ID); err != nil {
				errs = append(errs, fmt.Sprintf("delete load balancer %q: %v", lb.ID, err))
			}
		}
	}

	servers, err := srvClient.ListByTags(ctx, tags)
	if err != nil {
		errs = append(errs, err.Error())
	}

	securityGroups, err := sgClient.ListByTags(ctx, tags)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		for _, sg := range securityGroups {
			for _, srv := range servers {
				if err := sgClient.DetachFromServer(ctx, srv.ID, sg.ID); err != nil {
					errs = append(errs, fmt.Sprintf("detach security group %q from server %q: %v", sg.ID, srv.ID, err))
				}
			}
		}
	}

	for _, srv := range servers {
		if err := srvClient.DeleteByID(ctx, srv.ID); err != nil {
			errs = append(errs, fmt.Sprintf("delete server %q: %v", srv.ID, err))
		}
	}

	for _, sg := range securityGroups {
		if err := sgClient.DeleteByID(ctx, sg.ID); err != nil {
			errs = append(errs, fmt.Sprintf("delete security group %q: %v", sg.ID, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrTransient, strings.Join(errs, "; "))
	}
	return nil
}
