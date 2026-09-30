/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("serverStateCondition", func() {
	DescribeTable("maps server state and power status",
		func(state, powerStatus string, wantReady bool, wantReason, wantMessage string, wantWarn bool) {
			ready, reason, message, warn := serverStateCondition(state, powerStatus)
			Expect(ready).To(Equal(wantReady))
			Expect(reason).To(Equal(wantReason))
			Expect(message).To(Equal(wantMessage))
			Expect(warn).To(Equal(wantWarn))
		},
		Entry("active and running", "ACTIVE", "RUNNING", true, "", "", false),
		Entry("unknown state", "", "", true, "", "", false),
		Entry("active but crashed", "ACTIVE", "CRASHED", false, "InstanceCrashed",
			"server crashed (state ACTIVE, power status CRASHED)", true),
		Entry("active with power error", "ACTIVE", "ERROR", false, "InstancePowerError",
			"server power error (state ACTIVE, power status ERROR)", true),
		Entry("creating", "CREATING", "", false, "InstanceStarting", "server is starting (state CREATING)", false),
		Entry("rebooting", "REBOOTING", "RUNNING", false, "InstanceBusy",
			"server is temporarily unavailable (state REBOOTING, power status RUNNING)", false),
		Entry("stopping", "STOPPING", "RUNNING", false, "InstanceStopping",
			"server is stopping (state STOPPING, power status RUNNING)", false),
		Entry("stopped", "INACTIVE", "STOPPED", false, "InstanceStopped",
			"server is stopped (state INACTIVE, power status STOPPED)", true),
		Entry("deallocating", "DEALLOCATING", "STOPPED", false, "InstanceDeallocating",
			"server is being deallocated (state DEALLOCATING, power status STOPPED)", false),
		Entry("deallocated", "DEALLOCATED", "STOPPED", false, "InstanceDeallocated",
			"server is deallocated (state DEALLOCATED, power status STOPPED)", true),
		Entry("paused", "PAUSED", "", false, "InstancePaused", "server is paused (state PAUSED)", true),
		Entry("entering rescue", "RESCUING", "", false, "InstanceEnteringRescue",
			"server is entering rescue mode (state RESCUING)", false),
		Entry("leaving rescue", "UNRESCUING", "", false, "InstanceLeavingRescue",
			"server is leaving rescue mode (state UNRESCUING)", false),
		Entry("rescue", "RESCUE", "RUNNING", false, "InstanceInRescue",
			"server is in rescue mode (state RESCUE, power status RUNNING)", true),
		Entry("deleting", "DELETING", "", false, "InstanceDeleting", "server is being deleted (state DELETING)", false),
		Entry("error", "ERROR", "ERROR", false, "InstanceFailed", "server failed (state ERROR, power status ERROR)", true),
		Entry("undocumented state", "SOMETHING", "", false, "InstanceNotActive",
			"server is not active (state SOMETHING)", false),
	)
})
