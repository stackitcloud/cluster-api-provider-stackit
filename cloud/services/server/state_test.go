/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server_test

import (
	"testing"

	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
)

func TestStateCondition(t *testing.T) {
	tests := []struct {
		name        string
		state       string
		powerStatus string
		wantReady   bool
		wantReason  string
		wantMessage string
		wantWarn    bool
	}{
		{
			name:        "active and running",
			state:       "ACTIVE",
			powerStatus: "RUNNING",
			wantReady:   true,
			wantReason:  "InstanceActive",
			wantMessage: "server is active (state ACTIVE, power status RUNNING)",
			wantWarn:    false,
		},
		{
			name:        "unknown state",
			state:       "",
			powerStatus: "",
			wantReady:   true,
			wantReason:  "",
			wantMessage: "",
			wantWarn:    false,
		},
		{
			name:        "active but crashed",
			state:       "ACTIVE",
			powerStatus: "CRASHED",
			wantReady:   false,
			wantReason:  "InstanceCrashed",
			wantMessage: "server crashed (state ACTIVE, power status CRASHED)",
			wantWarn:    true,
		},
		{
			name:        "active with power error",
			state:       "ACTIVE",
			powerStatus: "ERROR",
			wantReady:   false,
			wantReason:  "InstancePowerError",
			wantMessage: "server power error (state ACTIVE, power status ERROR)",
			wantWarn:    true,
		},
		{
			name:        "creating",
			state:       "CREATING",
			powerStatus: "",
			wantReady:   false,
			wantReason:  "InstanceStarting",
			wantMessage: "server is starting (state CREATING)",
			wantWarn:    false,
		},
		{
			name:        "rebooting",
			state:       "REBOOTING",
			powerStatus: "RUNNING",
			wantReady:   false,
			wantReason:  "InstanceBusy",
			wantMessage: "server is temporarily unavailable (state REBOOTING, power status RUNNING)",
			wantWarn:    false,
		},
		{
			name:        "stopping",
			state:       "STOPPING",
			powerStatus: "RUNNING",
			wantReady:   false,
			wantReason:  "InstanceStopping",
			wantMessage: "server is stopping (state STOPPING, power status RUNNING)",
			wantWarn:    false,
		},
		{
			name:        "stopped",
			state:       "INACTIVE",
			powerStatus: "STOPPED",
			wantReady:   false,
			wantReason:  "InstanceStopped",
			wantMessage: "server is stopped (state INACTIVE, power status STOPPED)",
			wantWarn:    true,
		},
		{
			name:        "deallocating",
			state:       "DEALLOCATING",
			powerStatus: "STOPPED",
			wantReady:   false,
			wantReason:  "InstanceDeallocating",
			wantMessage: "server is being deallocated (state DEALLOCATING, power status STOPPED)",
			wantWarn:    false,
		},
		{
			name:        "deallocated",
			state:       "DEALLOCATED",
			powerStatus: "STOPPED",
			wantReady:   false,
			wantReason:  "InstanceDeallocated",
			wantMessage: "server is deallocated (state DEALLOCATED, power status STOPPED)",
			wantWarn:    true,
		},
		{
			name:        "paused",
			state:       "PAUSED",
			powerStatus: "",
			wantReady:   false,
			wantReason:  "InstancePaused",
			wantMessage: "server is paused (state PAUSED)",
			wantWarn:    true,
		},
		{
			name:        "entering rescue",
			state:       "RESCUING",
			powerStatus: "",
			wantReady:   false,
			wantReason:  "InstanceEnteringRescue",
			wantMessage: "server is entering rescue mode (state RESCUING)",
			wantWarn:    false,
		},
		{
			name:        "leaving rescue",
			state:       "UNRESCUING",
			powerStatus: "",
			wantReady:   false,
			wantReason:  "InstanceLeavingRescue",
			wantMessage: "server is leaving rescue mode (state UNRESCUING)",
			wantWarn:    false,
		},
		{
			name:        "rescue",
			state:       "RESCUE",
			powerStatus: "RUNNING",
			wantReady:   false,
			wantReason:  "InstanceInRescue",
			wantMessage: "server is in rescue mode (state RESCUE, power status RUNNING)",
			wantWarn:    true,
		},
		{
			name:        "deleting",
			state:       "DELETING",
			powerStatus: "",
			wantReady:   false,
			wantReason:  "InstanceDeleting",
			wantMessage: "server is being deleted (state DELETING)",
			wantWarn:    false,
		},
		{
			name:        "error",
			state:       "ERROR",
			powerStatus: "ERROR",
			wantReady:   false,
			wantReason:  "InstanceFailed",
			wantMessage: "server failed (state ERROR, power status ERROR)",
			wantWarn:    true,
		},
		{
			name:        "undocumented state",
			state:       "SOMETHING",
			powerStatus: "",
			wantReady:   false,
			wantReason:  "InstanceNotActive",
			wantMessage: "server is not active (state SOMETHING)",
			wantWarn:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, reason, message, warn := server.StateCondition(tt.state, tt.powerStatus)
			if ready != tt.wantReady {
				t.Errorf("StateCondition(%q, %q) ready = %v, want %v", tt.state, tt.powerStatus, ready, tt.wantReady)
			}
			if reason != tt.wantReason {
				t.Errorf("StateCondition(%q, %q) reason = %q, want %q", tt.state, tt.powerStatus, reason, tt.wantReason)
			}
			if message != tt.wantMessage {
				t.Errorf("StateCondition(%q, %q) message = %q, want %q", tt.state, tt.powerStatus, message, tt.wantMessage)
			}
			if warn != tt.wantWarn {
				t.Errorf("StateCondition(%q, %q) warn = %v, want %v", tt.state, tt.powerStatus, warn, tt.wantWarn)
			}
		})
	}
}
