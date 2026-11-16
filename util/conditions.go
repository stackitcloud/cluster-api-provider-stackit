/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package util

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func SetConditions(
	conditions *[]metav1.Condition,
	generation int64,
	status metav1.ConditionStatus,
	reason, message string,
	conditionTypes ...string,
) {
	for _, conditionType := range conditionTypes {
		meta.SetStatusCondition(conditions, metav1.Condition{
			Type:               conditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: generation,
		})
	}
}
