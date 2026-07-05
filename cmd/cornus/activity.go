package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/activity"
)

// ActivityCmd reads the server's flight records: what it and its caretakers
// were doing, and what they did not finish.
//
// It answers the question no other surface can. `cornus deploy`/`status` report
// what is true right now, and only for what the runtime still remembers; logs
// are ephemeral and go with the container. These records are on disk under the
// data dir, so they survive the process, the container, and the incident.
//
// Two read paths, because the normal case and the post-mortem case differ:
//
//   - by default, through the connection profile like every other command, since
//     the operator is almost never on the machine the server ran on. This still
//     answers post-mortem questions: the data dir is the thing deployments keep
//     persistent, so a REPLACEMENT server serves its predecessor's flight.
//   - with --local, straight off disk with no server involved, for the one case
//     the first cannot cover — nothing is running and nothing is coming back.
type ActivityCmd struct {
	Server     string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL. Falls back to the selected connection profile (see cornus config).'"`
	Local      bool   `kong:"name='local',help='Read the records straight off disk instead of asking a server. Use when no server is running: inside the container, or on the host after one is gone for good. The directory comes from the global --data-dir/CORNUS_DATA.'"`
	Since      string `kong:"name='since',help='Only records at or after this time (RFC3339, or a duration like 2h).'"`
	Kind       string `kong:"name='kind',help='Only this kind: server, caretaker, service, 9p-mount, build, deploy.'"`
	Unfinished bool   `kong:"name='unfinished',help='Only activities that began and never finished — an unclean exit, or an effect nobody owns.'"`
	Follow     bool   `kong:"name='follow',short='f',help='Print the records, then keep printing them as they are written. Ends on Ctrl-C.'"`
}

// Run fetches and renders the records.
func (c *ActivityCmd) Run(cli *CLI, d *cliout.Driver) error {
	since, err := parseSince(c.Since)
	if err != nil {
		return err
	}
	if c.Follow {
		return c.follow(cli, d, since)
	}
	events, live, err := c.read(cli, since)
	if err != nil {
		return err
	}
	if c.Unfinished {
		events = activity.UnfinishedFrom(events)
	}
	events = filterEvents(events, since, c.Kind)
	return d.Emit(activityResult{Events: events, Unfinished: c.Unfinished, Live: live})
}

// read returns the records and, when a server served them, that server's own
// instance id — the only way a reader can tell a lifetime that is open because
// the process is RUNNING from one open because it died. Reading local files
// answers no such thing, so --local returns no live instance.
func (c *ActivityCmd) read(cli *CLI, since time.Time) ([]activity.Event, string, error) {
	if c.Local {
		// The global --data-dir/CORNUS_DATA already names the server's data dir;
		// reusing it keeps one answer to "where does cornus keep its state".
		events, err := activity.Read(filepath.Join(cli.resolveConfig().DataDir, "activity"))
		return events, "", err
	}
	ctx := cli.rootContext()
	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return nil, "", err
	}
	defer cn.Cleanup()
	return cn.Client().Activity(ctx, since, c.Kind, c.Unfinished)
}

// follow prints the records and then keeps printing them as they are written,
// until the operator interrupts.
//
// Interrupting is the normal way this ends, so it exits 0: a watch that reported
// failure every time you stopped watching would make the command unusable in a
// script and alarming by hand.
func (c *ActivityCmd) follow(cli *CLI, d *cliout.Driver, since time.Time) error {
	if c.Unfinished {
		// Unfinished is resolved over the whole stream: a begin is unfinished only
		// until its end arrives. As a feed it would print records that the next
		// line makes false, with nothing printed to retract them.
		return fmt.Errorf("--follow and --unfinished cannot be combined: unfinished is resolved over the whole stream, so it is a snapshot, not a feed. Re-run without --follow, or follow and pair begin/end yourself")
	}
	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	emit := func(e activity.Event) error { return d.Emit(activityStreamEvent{Event: e}) }
	var err error
	if c.Local {
		err = activity.Follow(ctx, filepath.Join(cli.resolveConfig().DataDir, "activity"), 0, func(e activity.Event) error {
			if !matchEvent(e, since, c.Kind) {
				return nil
			}
			return emit(e)
		})
	} else {
		var cn *clientconn.Conn
		cn, err = cli.requireConn(c.Server)
		if err != nil {
			return err
		}
		defer cn.Cleanup()
		// The server applies since/kind, so nothing needs re-filtering here.
		_, err = cn.Client().ActivityFollow(ctx, since, c.Kind, emit)
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// parseSince turns the flag into a lower bound, naming the flag in the error so
// the message is actionable where the user typed it.
func parseSince(s string) (time.Time, error) {
	t, err := activity.ParseSince(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since: %w", err)
	}
	return t, nil
}

func filterEvents(events []activity.Event, since time.Time, kind string) []activity.Event {
	if since.IsZero() && kind == "" {
		return events
	}
	out := events[:0:0]
	for _, e := range events {
		if matchEvent(e, since, kind) {
			out = append(out, e)
		}
	}
	return out
}

// matchEvent is the per-record half of filterEvents, shared with the followed
// stream — where there is no set to filter, only one record at a time.
func matchEvent(e activity.Event, since time.Time, kind string) bool {
	if kind != "" && !strings.EqualFold(string(e.Kind), kind) {
		return false
	}
	if !since.IsZero() {
		ts := e.Unix()
		if ts.IsZero() || ts.Before(since) {
			return false
		}
	}
	return true
}

// activityResult renders the records: raw NDJSON in json mode (the machine
// interface, unchanged from what is on disk), a readable timeline otherwise.
type activityResult struct {
	Events     []activity.Event
	Unfinished bool
	// Live is the serving process's instance id, when a server served these.
	// Empty for --local, where nothing can vouch for what is still running.
	Live string
}

// MarshalJSON emits the events array so `--output json` is the records
// themselves rather than a wrapper around them.
func (r activityResult) MarshalJSON() ([]byte, error) {
	if r.Events == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Events)
}

func (r activityResult) Human(p cliout.Printer) {
	if len(r.Events) == 0 {
		if r.Unfinished {
			p.Line("no unfinished activities")
			return
		}
		p.Line("no activity records")
		return
	}
	// Group by incarnation so the output reads as flights rather than a flat
	// stream: this run started here, did these things, ended (or did not).
	for _, inst := range instancesOf(r.Events) {
		p.Line("")
		p.Line("%s", describeInstance(r.Events, inst, r.Live))
		for _, e := range r.Events {
			if e.Instance != inst {
				continue
			}
			p.Line("  %s", describeEvent(e))
		}
	}
}

func instancesOf(events []activity.Event) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if !seen[e.Instance] {
			seen[e.Instance] = true
			out = append(out, e.Instance)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return firstTS(events, out[i]) < firstTS(events, out[j]) })
	return out
}

func firstTS(events []activity.Event, instance string) string {
	for _, e := range events {
		if e.Instance == instance {
			return e.TS
		}
	}
	return ""
}

// describeInstance leads with the fact an investigator wants first: did this run
// end cleanly, or is it still going, or did it die?
func describeInstance(events []activity.Event, instance, live string) string {
	var proc string
	for _, e := range events {
		if e.Instance == instance {
			proc = e.Proc
			break
		}
	}
	known, clean := activity.CleanExit(events, instance)
	state := "no lifetime record"
	switch {
	case known && clean:
		state = "exited cleanly"
	case known && instance == live:
		// Its lifetime is open because it is still running, not because it died.
		state = "running"
	case known:
		state = "DID NOT EXIT CLEANLY"
	case instance == live:
		state = "running"
	}
	return fmt.Sprintf("%s %s (%s)", proc, instance, state)
}

// activityStreamEvent renders one record of a followed stream.
//
// It cannot use the grouped view: grouping is by incarnation, and an
// incarnation's verdict ("exited cleanly", "DID NOT EXIT CLEANLY") is only known
// once it has ended — which, live, is exactly what has not happened yet. So each
// line names its own writer instead of sitting under a heading that names it.
type activityStreamEvent struct {
	activity.Event
}

// MarshalJSON emits the bare record, so `--output json --follow` is an NDJSON
// feed of the same objects the one-shot read returns.
func (s activityStreamEvent) MarshalJSON() ([]byte, error) { return json.Marshal(s.Event) }

func (s activityStreamEvent) Human(p cliout.Printer) {
	p.Line("%s %s/%s %s", s.TS, s.Proc, s.Instance, describeEventTail(s.Event))
}

func describeEvent(e activity.Event) string {
	return e.TS + " " + describeEventTail(e)
}

// describeEventTail is everything about a record except when it happened, so the
// grouped view and the live stream can put different things in front of it.
func describeEventTail(e activity.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-9s %-5s", e.Kind, e.Phase)
	if e.Target != "" {
		fmt.Fprintf(&b, " %s", e.Target)
	}
	if e.Status != "" {
		fmt.Fprintf(&b, " [%s]", e.Status)
	}
	if e.Err != "" {
		fmt.Fprintf(&b, " %s", e.Err)
	}
	for _, k := range sortedAttrKeys(e.Attrs) {
		fmt.Fprintf(&b, " %s=%s", k, e.Attrs[k])
	}
	return b.String()
}

func sortedAttrKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
