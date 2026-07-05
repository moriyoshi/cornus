package composecli

import (
	"fmt"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
)

// serviceEvent is a structured per-service progress event: a "<service>  <tail>"
// line in plain/fancy mode, a JSON object in json mode. It replaces the old
// d.Info("%s  <action>", ...) calls, which baked the service name and a
// cosmetic two-space separator into a free-form message (opaque in json mode).
type serviceEvent struct {
	Service  string `json:"service"`
	Event    string `json:"event"`              // short verb: up | removed | forwarding | started | ...
	Detail   string `json:"detail,omitempty"`   // extra text (parenthetical, forward target, ...)
	Instance string `json:"instance,omitempty"` // instance id, for a state transition
	State    string `json:"state,omitempty"`    // instance state, for a state transition
	Running  int    `json:"running,omitempty"`  // running instance count, for "up"
	Total    int    `json:"total,omitempty"`    // total instance count, for "up"
}

// Human renders the "<service>  <tail>" line, reproducing the wording the
// commands used before the migration.
func (e serviceEvent) Human(p cliout.Printer) {
	var tail string
	switch e.Event {
	case "up":
		if e.Total > 0 {
			tail = fmt.Sprintf("up (%d/%d running)", e.Running, e.Total)
		} else {
			tail = "up (" + e.Detail + ")"
		}
	case "forwarding":
		tail = "forwarding " + e.Detail
	case "up-to-date":
		tail = "is up-to-date (" + e.Detail + ")"
	case "recreated":
		tail = "recreated: " + e.Detail
	case "transition":
		tail = e.Instance + ": " + e.State
	default: // removed, started, stopped, restarted, ...
		tail = e.Event
		if e.Detail != "" {
			tail += " " + e.Detail
		}
	}
	p.Line("%s  %s", e.Service, tail)
}

// svcUp reports a service that came up, with its running/total instance counts.
func svcUp(name string, st api.DeployStatus) serviceEvent {
	running := 0
	for _, in := range st.Instances {
		if in.Running {
			running++
		}
	}
	return serviceEvent{Service: name, Event: "up", Running: running, Total: len(st.Instances)}
}

// svcEvent reports a simple per-service event (an optional detail follows the verb).
func svcEvent(name, event, detail string) serviceEvent {
	return serviceEvent{Service: name, Event: event, Detail: detail}
}
