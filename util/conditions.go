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

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
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

// CredentialFailureResult records the failure and decides how to retry.
//
// Invalid credentials need an operator to fix the Secret, so they requeue on a
// slow timer rather than returning an error and spinning through the controller
// backoff. The reconcile reads the Secret again, which is what picks up the fix.
func CredentialFailureResult(
	conditions *[]metav1.Condition,
	generation int64,
	err error,
	requeueAfter time.Duration,
	conditionTypes ...string,
) (ctrl.Result, error) {
	SetConditions(conditions, generation, metav1.ConditionFalse, "CredentialsInvalid", err.Error(), conditionTypes...)
	if shared.IsUnauthorized(err) || shared.IsInvalidInput(err) || errors.Is(err, ErrCredentialsInvalid) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
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
	if shared.IsRetryable(err) {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	if returnError {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
