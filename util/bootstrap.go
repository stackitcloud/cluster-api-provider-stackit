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

	corev1 "k8s.io/api/core/v1"
)

// Reasons returned to callers of ExtractBootstrapData. They map to condition
// reasons surfaced on StackitMachine.
const (
	BootstrapReasonInvalid = "BootstrapDataInvalid"
)

// ErrBootstrapDataInvalid is returned when the bootstrap Secret exists but
// does not contain one of the expected keys.
var ErrBootstrapDataInvalid = errors.New("bootstrap data invalid: secret has no \"value\" or \"userData\" key")

// ExtractBootstrapData returns the bootstrap payload from a Secret produced
// by CABPK/KubeadmConfig. Per spec section 13.2 it checks "value" first, then
// "userData". It returns ErrBootstrapDataInvalid when neither key is present
// or both are empty.
func ExtractBootstrapData(secret *corev1.Secret) ([]byte, error) {
	if data, ok := secret.Data["value"]; ok && len(data) > 0 {
		return data, nil
	}
	if data, ok := secret.Data["userData"]; ok && len(data) > 0 {
		return data, nil
	}
	return nil, ErrBootstrapDataInvalid
}
