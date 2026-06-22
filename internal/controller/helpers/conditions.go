/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package helpers

import (
	"errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/cloud"
	"github.com/stackitcloud/cluster-api-provider-stackit/pkg/util"
)

func SetConditions(
	conditions *[]metav1.Condition,
	generation int64,
	status metav1.ConditionStatus,
	reason, message string,
	conditionTypes ...string,
) {
	for _, conditionType := range conditionTypes {
		util.SetCondition(conditions, conditionType, status, reason, message, generation)
	}
}

func CredentialFailureResult(
	conditions *[]metav1.Condition,
	generation int64,
	err error,
	conditionTypes ...string,
) (ctrl.Result, error) {
	SetConditions(conditions, generation, metav1.ConditionFalse, "CredentialsInvalid", err.Error(), conditionTypes...)
	if cloud.IsUnauthorized(err) || cloud.IsInvalidInput(err) || errors.Is(err, util.ErrCredentialsInvalid) {
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
