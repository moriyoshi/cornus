package server

import (
	"os"
	"strings"
)

// advertiseURL is the URL a caretaker, mount agent, or companion dials cornus
// back on: CORNUS_ADVERTISE_URL. agentImage is the cornus-embedding image those
// companions run: CORNUS_AGENT_IMAGE. Both are env-only (there is no flag for
// either) and every consumer in this package must go through these accessors.
//
// Why they are not memoized in New, which looks like the obvious cleanup: the
// advertised URL is the address OTHERS reach this server at, and that is not
// always known when the Server is constructed. A Server built ahead of its
// listener — which is exactly what httptest does, and what the two-replica
// relay tests depend on — would memoize "" and then reject every attachment
// with "requires CORNUS_ADVERTISE_URL" no matter what the environment later
// said. Reading per request costs one getenv on a path that is already doing a
// deploy, and in a real server the value is fixed before exec anyway. If this
// ever moves to a flag or to auto-detection from the in-cluster Service, these
// two functions are the only places that change.
//
// Both TRIM. Whitespace matters because these values are pasted into
// environments by hand and by YAML: a trailing newline from a folded scalar or
// a stray space in an env file used to make the same variable mean different
// things in different places — the telemetry path trimmed and worked, while the
// mount and egress paths treated " " as set and handed a malformed RelayURL to
// the caretaker, which fails later and further away. Trimming in one place is
// what makes "set" mean the same thing everywhere.
func advertiseURL() string { return strings.TrimSpace(os.Getenv("CORNUS_ADVERTISE_URL")) }

// agentImage returns the cornus-embedding companion image. See advertiseURL for
// why this is read per use and trimmed.
func agentImage() string { return strings.TrimSpace(os.Getenv("CORNUS_AGENT_IMAGE")) }
