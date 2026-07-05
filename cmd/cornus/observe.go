package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/client"
	"cornus/pkg/obsstore"
)

// ObserveCmd reads a WORKLOAD's recorded telemetry from the server's built-in
// observability store.
//
// The boundary against the neighbouring commands is worth stating, because all
// three answer "what happened" and only one of them is about the user's app:
//
//   - `cornus activity` reads cornus's own flight records — what the SERVER and
//     its caretakers were doing.
//   - `cornus compose logs` reads one project's services, live from the runtime
//     or from this same store (`--from=store`), rendered as a tail.
//   - `cornus observe` reads across everything the store holds, for every
//     workload, and adds the nouns a tail cannot carry: traces, metrics, and
//     arbitrary queries.
//
// Every subcommand needs a server with the store enabled; without one they fail
// with a message naming the remedy rather than an empty result, because an empty
// result would read as "nothing happened".
type ObserveCmd struct {
	Logs    ObserveLogsCmd    `kong:"cmd,help='Search recorded workload logs across every deployment.'"`
	Traces  ObserveTracesCmd  `kong:"cmd,help='Search recorded traces (slowest, failing, by service).'"`
	Trace   ObserveTraceCmd   `kong:"cmd,help='Show one trace as a waterfall of its spans.'"`
	Metrics ObserveMetricsCmd `kong:"cmd,help='Evaluate a PromQL range query over recorded metrics.'"`
	Query   ObserveQueryCmd   `kong:"cmd,help='Run raw SQL against the store (tables: logs, spans, metrics_*).'"`
	Status  ObserveStatusCmd  `kong:"cmd,help='Report what the store is holding, how far back, and whether it is dropping records.'"`
}

// observeConn resolves the server connection every subcommand needs.
func observeConn(cli *CLI, server string) (*client.Client, func(), error) {
	cn, err := cli.requireConn(server)
	if err != nil {
		return nil, nil, err
	}
	return cn.Client(), cn.Cleanup, nil
}

// --- logs -------------------------------------------------------------------

type ObserveLogsCmd struct {
	Server   string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL. Falls back to the selected connection profile.'"`
	Service  string `kong:"name='service',help='Only this service (the deployment name).'"`
	Match    string `kong:"name='match',help='Only records whose body matches this text.'"`
	Severity string `kong:"name='severity',help='Only records at or above this level: debug, info, warn, error, fatal.'"`
	Stream   string `kong:"name='stream',default='',enum=',stdout,stderr',help='Only records from this container stream.'"`
	Replica  string `kong:"name='replica',help='Only records from this instance ordinal of a scaled workload (0-based). Default: every replica.'"`
	Trace    string `kong:"name='trace',help='Only records correlated to this trace id — the logs belonging to one request.'"`
	Since    string `kong:"name='since',help='Only records at or after this time (RFC3339, Unix seconds, or a duration like 2h).'"`
	Until    string `kong:"name='until',help='Only records before this time.'"`
	Limit    int    `kong:"name='limit',default='200',help='Maximum records to return.'"`
	Oldest   bool   `kong:"name='oldest',help='Return the oldest matching records instead of the most recent.'"`
}

func (c *ObserveLogsCmd) Run(cli *CLI, d *cliout.Driver) error {
	cl, cleanup, err := observeConn(cli, c.Server)
	if err != nil {
		return err
	}
	defer cleanup()

	entries, err := cl.ObsLogs(cli.rootContext(), client.ObsLogQuery{
		Service:  c.Service,
		Match:    c.Match,
		Severity: c.Severity,
		Stream:   c.Stream,
		Replica:  c.Replica,
		TraceID:  c.Trace,
		Since:    c.Since,
		Until:    c.Until,
		Limit:    c.Limit,
		// Ask newest-first so a limit keeps the most RECENT records, then
		// present them oldest-first, which is how logs read.
		Newest: !c.Oldest,
	})
	if err != nil {
		return err
	}
	if !c.Oldest {
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time.Before(entries[j].Time) })
	}
	return d.Emit(observeLogsResult{Entries: entries})
}

type observeLogsResult struct {
	Entries []obsstore.LogEntry
}

func (r observeLogsResult) MarshalJSON() ([]byte, error) {
	if r.Entries == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Entries)
}

func (r observeLogsResult) Human(p cliout.Printer) {
	if len(r.Entries) == 0 {
		p.Line("no matching records")
		return
	}
	for _, e := range r.Entries {
		stamp := e.Time.UTC().Format(time.RFC3339)
		service := e.Service
		if service == "" {
			service = "-"
		}
		if e.SeverityText != "" {
			p.Line("%s  %-12s %-5s %s", stamp, service, e.SeverityText, e.Body)
			continue
		}
		p.Line("%s  %-12s %s", stamp, service, e.Body)
	}
}

// --- traces -----------------------------------------------------------------

type ObserveTracesCmd struct {
	Server      string        `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL.'"`
	Service     string        `kong:"name='service',help='Only traces whose spans belong to this service.'"`
	Name        string        `kong:"name='name',help='Only traces with a span of this name.'"`
	Status      string        `kong:"name='status',help='Only traces with this span status (e.g. error).'"`
	Kind        string        `kong:"name='kind',help='Only traces with a span of this kind (server, client, producer, consumer, internal).'"`
	MinDuration time.Duration `kong:"name='min-duration',help='Only traces at least this long — how you find the slow ones.'"`
	MaxDuration time.Duration `kong:"name='max-duration',help='Only traces no longer than this.'"`
	Since       string        `kong:"name='since',help='Only traces starting at or after this time.'"`
	Until       string        `kong:"name='until',help='Only traces starting before this time.'"`
	Limit       int           `kong:"name='limit',default='50',help='Maximum traces to return.'"`
}

func (c *ObserveTracesCmd) Run(cli *CLI, d *cliout.Driver) error {
	cl, cleanup, err := observeConn(cli, c.Server)
	if err != nil {
		return err
	}
	defer cleanup()

	out, err := cl.ObsTraces(cli.rootContext(), client.ObsTraceQuery{
		Service:     c.Service,
		Name:        c.Name,
		Status:      c.Status,
		Kind:        c.Kind,
		MinDuration: c.MinDuration,
		MaxDuration: c.MaxDuration,
		Since:       c.Since,
		Until:       c.Until,
		Limit:       c.Limit,
	})
	if err != nil {
		return err
	}
	return d.Emit(observeTracesResult{Traces: out})
}

type observeTracesResult struct {
	Traces []obsstore.TraceSummary
}

func (r observeTracesResult) MarshalJSON() ([]byte, error) {
	if r.Traces == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Traces)
}

func (r observeTracesResult) Human(p cliout.Printer) {
	if len(r.Traces) == 0 {
		p.Line("no matching traces")
		return
	}
	p.Line("%-32s  %-20s  %-10s  %5s  %s", "TRACE", "ROOT", "DURATION", "SPANS", "STATUS")
	for _, t := range r.Traces {
		root := t.RootName
		if t.RootService != "" {
			root = t.RootService + "/" + t.RootName
		}
		if root == "" || root == "/" {
			// The root span itself is not in the store — a partially collected
			// trace. Say so rather than printing an empty column.
			root = "(root not recorded)"
		}
		status := "ok"
		if t.Error {
			status = "error"
		}
		p.Line("%-32s  %-20s  %-10s  %5d  %s", t.TraceID, truncate(root, 20), t.Duration.Round(time.Microsecond), t.SpanCount, status)
	}
	p.Line("")
	p.Line("show one with: cornus observe trace <TRACE>")
}

// --- trace ------------------------------------------------------------------

type ObserveTraceCmd struct {
	ID     string `kong:"arg,help='Trace id (hex), as printed by cornus observe traces.'"`
	Server string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL.'"`
}

func (c *ObserveTraceCmd) Run(cli *CLI, d *cliout.Driver) error {
	cl, cleanup, err := observeConn(cli, c.Server)
	if err != nil {
		return err
	}
	defer cleanup()

	spans, err := cl.ObsTrace(cli.rootContext(), c.ID)
	if err != nil {
		return err
	}
	return d.Emit(observeTraceResult{ID: c.ID, Spans: spans})
}

type observeTraceResult struct {
	ID    string
	Spans []obsstore.Span
}

func (r observeTraceResult) MarshalJSON() ([]byte, error) {
	if r.Spans == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Spans)
}

// Human renders the trace as an indented waterfall.
//
// The indentation comes from obsstore.AssembleTrace, which surfaces an orphan
// span as a root rather than dropping it — a partially collected trace is
// exactly when someone is reading this, so every span it holds must appear.
func (r observeTraceResult) Human(p cliout.Printer) {
	if len(r.Spans) == 0 {
		p.Line("trace %s has no recorded spans", r.ID)
		return
	}
	roots := obsstore.AssembleTrace(r.Spans)

	// Scale the bars against the whole trace's span, so the shape shows where
	// the time actually went rather than how long each span was in isolation.
	var first, last time.Time
	for _, s := range r.Spans {
		if first.IsZero() || s.Start.Before(first) {
			first = s.Start
		}
		if end := s.Start.Add(s.Duration); last.IsZero() || end.After(last) {
			last = end
		}
	}
	total := last.Sub(first)
	if total <= 0 {
		total = time.Nanosecond
	}

	p.Line("trace %s — %d spans over %s", r.ID, len(r.Spans), total.Round(time.Microsecond))
	p.Line("")
	var walk func(n *obsstore.Node, depth int)
	walk = func(n *obsstore.Node, depth int) {
		label := strings.Repeat("  ", depth) + n.Name
		svc := n.Service
		if svc == "" {
			svc = "-"
		}
		mark := ""
		if n.StatusCode != "" && !strings.EqualFold(n.StatusCode, "ok") && !strings.EqualFold(n.StatusCode, "unset") {
			mark = "  !" + n.StatusCode
		}
		p.Line("%-42s %-14s %10s  %s%s",
			truncate(label, 42), truncate(svc, 14),
			n.Duration.Round(time.Microsecond),
			waterfallBar(n.Start.Sub(first), n.Duration, total), mark)
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
}

// waterfallBar renders one span's position and extent within the trace as a
// fixed-width bar, so the visual reading is "when, and for how long".
func waterfallBar(offset, dur, total time.Duration) string {
	const width = 30
	scale := func(d time.Duration) int {
		if d <= 0 {
			return 0
		}
		n := int(float64(d) / float64(total) * width)
		if n > width {
			return width
		}
		return n
	}
	lead := scale(offset)
	span := scale(dur)
	// A span shorter than one cell still happened; give it a cell so it does not
	// vanish from the picture.
	if span == 0 {
		span = 1
	}
	if lead+span > width {
		lead = width - span
	}
	if lead < 0 {
		lead = 0
	}
	return strings.Repeat(" ", lead) + strings.Repeat("█", span)
}

// --- metrics ----------------------------------------------------------------

type ObserveMetricsCmd struct {
	Query  string        `kong:"arg,help='PromQL range query, e.g. rate(http_requests_total[5m]).'"`
	Server string        `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL.'"`
	Since  string        `kong:"name='since',default='1h',help='Start of the range.'"`
	Until  string        `kong:"name='until',help='End of the range (default: now).'"`
	Step   time.Duration `kong:"name='step',default='1m',help='Resolution of the returned series.'"`
}

func (c *ObserveMetricsCmd) Run(cli *CLI, d *cliout.Driver) error {
	cl, cleanup, err := observeConn(cli, c.Server)
	if err != nil {
		return err
	}
	defer cleanup()

	series, err := cl.ObsMetrics(cli.rootContext(), c.Query, c.Since, c.Until, c.Step)
	if err != nil {
		return err
	}
	return d.Emit(observeMetricsResult{Series: series})
}

type observeMetricsResult struct {
	Series []obsstore.Series
}

func (r observeMetricsResult) MarshalJSON() ([]byte, error) {
	if r.Series == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Series)
}

func (r observeMetricsResult) Human(p cliout.Printer) {
	if len(r.Series) == 0 {
		p.Line("no matching series")
		return
	}
	for _, s := range r.Series {
		p.Line("%s", labelSetString(s.Labels))
		for _, pt := range s.Points {
			p.Line("  %s  %g", pt.T.UTC().Format(time.RFC3339), pt.V)
		}
	}
}

// labelSetString renders a label set in PromQL's own notation, key-sorted so the
// same series always prints the same way.
func labelSetString(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// --- query ------------------------------------------------------------------

type ObserveQueryCmd struct {
	SQL    string `kong:"arg,help='SQL over the store. Tables: logs, spans, metrics_gauge, metrics_sum, metrics_histogram.'"`
	Server string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL.'"`
}

func (c *ObserveQueryCmd) Run(cli *CLI, d *cliout.Driver) error {
	cl, cleanup, err := observeConn(cli, c.Server)
	if err != nil {
		return err
	}
	defer cleanup()

	rows, err := cl.ObsQuery(cli.rootContext(), c.SQL)
	if err != nil {
		return err
	}
	return d.Emit(observeQueryResult{Rows: rows})
}

type observeQueryResult struct {
	Rows []obsstore.Row
}

func (r observeQueryResult) MarshalJSON() ([]byte, error) {
	if r.Rows == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Rows)
}

func (r observeQueryResult) Human(p cliout.Printer) {
	if len(r.Rows) == 0 {
		p.Line("no rows")
		return
	}
	// Column order comes from the first row's keys, sorted: a map has no order,
	// and an unstable one would make successive runs of the same query look
	// different.
	cols := make([]string, 0, len(r.Rows[0]))
	for k := range r.Rows[0] {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	p.Line("%s", strings.Join(cols, "\t"))
	for _, row := range r.Rows {
		vals := make([]string, 0, len(cols))
		for _, c := range cols {
			vals = append(vals, fmt.Sprintf("%v", row[c]))
		}
		p.Line("%s", strings.Join(vals, "\t"))
	}
}

// --- status -----------------------------------------------------------------

type ObserveStatusCmd struct {
	Server string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL.'"`
}

func (c *ObserveStatusCmd) Run(cli *CLI, d *cliout.Driver) error {
	cl, cleanup, err := observeConn(cli, c.Server)
	if err != nil {
		return err
	}
	defer cleanup()

	st, err := cl.ObsStatus(cli.rootContext())
	if err != nil {
		return err
	}
	return d.Emit(observeStatusResult{Status: st})
}

type observeStatusResult struct {
	Status obsstore.Status
}

func (r observeStatusResult) MarshalJSON() ([]byte, error) { return json.Marshal(r.Status) }

func (r observeStatusResult) Human(p cliout.Printer) {
	s := r.Status
	p.Line("directory   %s", s.Dir)
	if s.Retention > 0 {
		p.Line("retention   %s", s.Retention)
	} else {
		p.Line("retention   unlimited (bounded only by the size cap)")
	}
	if s.MaxBytes > 0 {
		p.Line("size cap    %s", humanBytes(s.MaxBytes))
	} else {
		p.Line("size cap    unlimited")
	}
	p.Line("buffered    %s", humanBytes(s.BufferBytes))
	p.Line("")
	if len(s.Tables) == 0 {
		p.Line("nothing recorded yet")
	} else {
		p.Line("%-20s %10s %10s  %s", "TABLE", "ROWS", "SEGMENTS", "OLDEST")
		for _, t := range s.Tables {
			oldest := "-"
			if !t.Oldest.IsZero() {
				oldest = t.Oldest.UTC().Format(time.RFC3339)
			}
			p.Line("%-20s %10d %10d  %s", t.Name, t.Rows, t.Segments, oldest)
		}
	}
	if m := s.Metrics; m != nil {
		p.Line("")
		p.Line("metrics     sampling %d replica(s) every %s", m.Replicas, m.Interval)
		p.Line("  recorded  %d readings", m.Sampled)
		// Named separately because they point at different places. A failing
		// sample means the BACKEND would not answer (missing RBAC on kubernetes,
		// an unreachable daemon); a dropped one means the reading was taken and
		// then shed, which is about capacity. Both look like an empty series.
		if m.Failed > 0 {
			p.Line("  FAILED    %d samples the backend refused — check `cornus daemon preflight`", m.Failed)
		}
		if m.Dropped > 0 {
			p.Line("  DROPPED   %d readings shed under load", m.Dropped)
		}
		if m.Replicas > 0 && m.Sampled == 0 && m.Failed == 0 {
			p.Line("  (no readings yet; the first cycle may not have completed)")
		}
	}

	if e := s.Export; e != nil {
		p.Line("")
		p.Line("re-export   %s", e.Endpoint)
		p.Line("  sent      %d", e.Sent)
		// The two failure counters mean different things: dropped is cornus
		// shedding because the forwarder fell behind, failed is the upstream
		// refusing or being unreachable. Tuning fixes one; the other needs
		// someone to look at the backend.
		if e.Dropped > 0 {
			p.Line("  DROPPED   %d shed because the forwarder fell behind the upstream", e.Dropped)
		}
		if e.Failed > 0 {
			p.Line("  FAILED    %d rejected or unreachable at the upstream", e.Failed)
		}
		if e.Dropped == 0 && e.Failed == 0 {
			p.Line("  dropped   0, failed 0")
		}
	}

	// Dropped last and unconditionally, because it is the number that decides
	// whether an empty query result means "nothing happened" or "the evidence
	// was shed".
	p.Line("")
	if s.Dropped > 0 {
		p.Line("DROPPED     %d records shed under load — an empty query result may be missing data", s.Dropped)
	} else {
		p.Line("dropped     0")
	}
	if s.Errors > 0 {
		p.Line("errors      %d rejected on ingest", s.Errors)
	}
}

// --- shared helpers ---------------------------------------------------------

// truncate shortens s to at most n characters, marking the cut with an ellipsis.
// It counts runes, not bytes, so a multi-byte service name is not sliced through
// the middle of a character.
//
// (humanBytes lives in storage.go and is shared.)
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
