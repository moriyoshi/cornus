package composecli

import "cornus/cmd/cornus/internal/clientagent"

// The compose `up -d` / `down` path drives the unified client agent
// (cmd/cornus/internal/clientagent). These aliases keep the call sites reading in
// compose terms; the wire types and client stub live in clientagent.

type daemonService = clientagent.Service

const (
	svcStatusUpToDate  = clientagent.StatusUpToDate
	svcStatusRecreated = clientagent.StatusRecreated
)

// agentHoldsProject reports whether the background agent currently holds the
// named compose project (its mount sessions live there), so a lifecycle action
// on a mounted service must go through `down` rather than an in-place stop.
func agentHoldsProject(project string) bool {
	inv, err := clientagent.Status()
	if err != nil || inv == nil {
		return false
	}
	_, ok := inv.Projects[project]
	return ok
}
