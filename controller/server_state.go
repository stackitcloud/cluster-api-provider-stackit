/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import "fmt"

type serverStateInfo struct {
	reason      string
	description string
	// warn marks states that do not resolve on their own. Their reason must
	// differ from the transitional state leading there, because warnings are
	// deduplicated by reason.
	warn bool
}

// serverStates maps the documented STACKIT server states other than ACTIVE.
var serverStates = map[string]serverStateInfo{
	"CREATING":     {"InstanceStarting", "server is starting", false},
	"STARTING":     {"InstanceStarting", "server is starting", false},
	"REBOOT":       {"InstanceBusy", "server is temporarily unavailable", false},
	"REBOOTING":    {"InstanceBusy", "server is temporarily unavailable", false},
	"REBUILD":      {"InstanceBusy", "server is temporarily unavailable", false},
	"REBUILDING":   {"InstanceBusy", "server is temporarily unavailable", false},
	"RESIZING":     {"InstanceBusy", "server is temporarily unavailable", false},
	"MIGRATING":    {"InstanceBusy", "server is temporarily unavailable", false},
	"UPDATING":     {"InstanceBusy", "server is temporarily unavailable", false},
	"RESTORING":    {"InstanceBusy", "server is temporarily unavailable", false},
	"SNAPSHOTTING": {"InstanceBusy", "server is temporarily unavailable", false},
	"BACKING-UP":   {"InstanceBusy", "server is temporarily unavailable", false},
	"STOPPING":     {"InstanceStopping", "server is stopping", false},
	"INACTIVE":     {"InstanceStopped", "server is stopped", true},
	"DEALLOCATING": {"InstanceDeallocating", "server is being deallocated", false},
	"DEALLOCATED":  {"InstanceDeallocated", "server is deallocated", true},
	"PAUSED":       {"InstancePaused", "server is paused", true},
	"RESCUING":     {"InstanceEnteringRescue", "server is entering rescue mode", false},
	"RESCUE":       {"InstanceInRescue", "server is in rescue mode", true},
	"UNRESCUING":   {"InstanceLeavingRescue", "server is leaving rescue mode", false},
	"DELETING":     {"InstanceDeleting", "server is being deleted", false},
	"DELETED":      {"InstanceDeleting", "server is deleted", false},
	"ERROR":        {"InstanceFailed", "server failed", true},
}

// serverStateCondition maps a server state and power status to a condition
// reason and message. ready is true when the server can be used.
func serverStateCondition(state, powerStatus string) (ready bool, reason, message string, warn bool) {
	details := "state " + state
	if powerStatus != "" {
		details += ", power status " + powerStatus
	}

	if state == "" || state == "ACTIVE" {
		switch powerStatus {
		case "CRASHED":
			return false, "InstanceCrashed", fmt.Sprintf("server crashed (%s)", details), true
		case "ERROR":
			return false, "InstancePowerError", fmt.Sprintf("server power error (%s)", details), true
		}
		return true, "", "", false
	}

	info, ok := serverStates[state]
	if !ok {
		info = serverStateInfo{"InstanceNotActive", "server is not active", false}
	}
	return false, info.reason, fmt.Sprintf("%s (%s)", info.description, details), info.warn
}
