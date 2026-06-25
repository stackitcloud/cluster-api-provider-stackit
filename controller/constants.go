/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import "time"

const (
	defaultAPIServerPort int32 = 6443

	cloudInitRefKindSecret = "Secret"

	retryableErrorRequeueAfter = 5 * time.Second
)
