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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
)

// SetCondition adds or updates a condition on conditions.
//
// The LastTransitionTime is only refreshed when the status actually changes,
// so callers can call this unconditionally on every reconcile.
func SetCondition(
	conditions *[]metav1.Condition,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
	observedGeneration ...int64,
) {
	now := metav1.Now()
	var generation int64
	if len(observedGeneration) > 0 {
		generation = observedGeneration[0]
	}
	for i, c := range *conditions {
		if c.Type != condType {
			continue
		}
		if c.Status == status && c.Reason == reason && c.Message == message && c.ObservedGeneration == generation {
			return
		}
		(*conditions)[i].Status = status
		(*conditions)[i].Reason = reason
		(*conditions)[i].Message = message
		(*conditions)[i].ObservedGeneration = generation
		if c.Status != status {
			(*conditions)[i].LastTransitionTime = now
		}
		return
	}
	*conditions = append(*conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
		LastTransitionTime: now,
	})
}

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

func CredentialFailureResult(
	conditions *[]metav1.Condition,
	generation int64,
	err error,
	conditionTypes ...string,
) (ctrl.Result, error) {
	SetConditions(conditions, generation, metav1.ConditionFalse, "CredentialsInvalid", err.Error(), conditionTypes...)
	if cloud.IsUnauthorized(err) || cloud.IsInvalidInput(err) || errors.Is(err, ErrCredentialsInvalid) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, err
}

func CloudFailureResult(
	conditions *[]metav1.Condition,
	generation int64,
	reason string,
	err error,
	requeueAfter time.Duration,
	returnError bool,
	conditionTypes ...string,
) (ctrl.Result, error) {
	SetConditions(conditions, generation, metav1.ConditionFalse, reason, err.Error(), conditionTypes...)
	if cloud.IsRetryable(err) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	if returnError {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
