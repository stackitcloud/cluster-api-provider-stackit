/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bastion

import (
	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

func Input(stackitCluster *infrav1.StackitCluster, cloudInit []byte) cloud.BastionInput {
	deleteOnTermination := true
	if stackitCluster.Spec.Bastion.RootVolume.DeleteOnTermination != nil {
		deleteOnTermination = *stackitCluster.Spec.Bastion.RootVolume.DeleteOnTermination
	}

	tags := util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
	tags[util.LabelResourceRole] = util.ResourceRoleBastion

	return cloud.BastionInput{
		Name:         stackitCluster.Name + "-bastion",
		ProjectID:    stackitCluster.Spec.ProjectID,
		Region:       stackitCluster.Spec.Region,
		NetworkID:    stackitCluster.Spec.Network.ID,
		ImageID:      stackitCluster.Spec.Bastion.ImageID,
		MachineType:  stackitCluster.Spec.Bastion.MachineType,
		SSHKeyName:   stackitCluster.Spec.Bastion.SSHKeyName,
		AllowedCIDRs: stackitCluster.Spec.Bastion.AllowedCIDRs,
		Tags:         tags,
		RootVolume: cloud.RootVolumeInput{
			SizeGiB:             stackitCluster.Spec.Bastion.RootVolume.SizeGiB,
			PerformanceClass:    stackitCluster.Spec.Bastion.RootVolume.PerformanceClass,
			DeleteOnTermination: deleteOnTermination,
		},
		CloudInit: cloudInit,
	}
}

func NodeSSHAccessTags(stackitCluster *infrav1.StackitCluster) map[string]string {
	tags := util.ClusterTags(stackitCluster.Name, stackitCluster.Namespace, stackitCluster.Spec.AdditionalLabels)
	tags[util.LabelResourceRole] = util.ResourceRoleNodeSSH
	return tags
}
