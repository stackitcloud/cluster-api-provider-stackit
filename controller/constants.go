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

	// deleteRequeueAfter paces the wait for dependent objects to disappear
	// during deletion. Matches what Cluster API and the other infrastructure
	// providers use for the same purpose.
	deleteRequeueAfter = 5 * time.Second
)
