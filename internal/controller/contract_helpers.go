/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

func reconciliationPaused(cluster *clusterv1.Cluster, obj client.Object) (bool, string) {
	if cluster == nil {
		if annotations.HasPaused(obj) {
			return true, "object has the cluster.x-k8s.io/paused annotation"
		}
		return false, ""
	}

	var reasons []string
	if ptr.Deref(cluster.Spec.Paused, false) {
		reasons = append(reasons, "Cluster spec.paused is set to true")
	}
	if annotations.HasPaused(obj) {
		reasons = append(reasons, "object has the cluster.x-k8s.io/paused annotation")
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, ", ")
}

func setPausedCondition(conditions *[]metav1.Condition, generation int64, paused bool, message string) {
	if paused {
		util.SetCondition(conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason, message, generation)
		return
	}
	util.SetCondition(conditions, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason, "", generation)
}
