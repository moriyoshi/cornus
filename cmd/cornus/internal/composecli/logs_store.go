package composecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/obsstore"
)

// Reading `compose logs` out of the observability store.
//
// The output is deliberately indistinguishable from the runtime path: the same
// service prefixes, the same stdout/stderr split, the same ordering. A user who
// did not pass --from should not be able to tell which source answered, because
// the question they asked ("what did this service print?") is the same one.
//
// The one visible difference is what the store can additionally do — search and
// level filtering — and those are opt-in flags that name themselves.

// storeLogs renders recorded logs for the selected services.
//
// Unlike the runtime path, which streams each service concurrently, this reads
// every service and merges by timestamp. That is affordable because the result
// is bounded and already materialized, and it is what makes multi-service output
// readable: interleaved by when things actually happened rather than by which
// query returned first.
func (c *LogsCmd) storeLogs(ctx context.Context, rt *runtime, names []string, opts api.LogOptions, prefix bool, d *cliout.Driver) error {
	if rt.client == nil {
		return errors.New("reading recorded logs needs a cornus server connection")
	}

	width := 0
	if prefix {
		for _, n := range names {
			if len(n) > width {
				width = len(n)
			}
		}
	}

	type tagged struct {
		service string
		entry   obsstore.LogEntry
	}
	var all []tagged
	for _, name := range names {
		q := client.ObsLogQuery{
			Service:  rt.plans[name].Resource,
			Match:    c.Match,
			Severity: c.Severity,
			Since:    opts.Since,
			Until:    opts.Until,
			Limit:    storeTailLimit(opts.Tail),
			// Ask newest-first so a `--tail N` keeps the LAST n lines rather
			// than the first n, then restore chronological order below. Taking
			// the oldest n would answer a different question entirely.
			Newest: true,
		}
		entries, err := rt.client.ObsLogs(ctx, q)
		if err != nil {
			if errors.Is(err, client.ErrObsUnavailable) {
				return storeUnavailableError(c)
			}
			return fmt.Errorf("reading recorded logs for %s: %w", name, err)
		}
		for _, e := range entries {
			all = append(all, tagged{service: name, entry: e})
		}
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].entry.Time.Equal(all[j].entry.Time) {
			return all[i].service < all[j].service
		}
		return all[i].entry.Time.Before(all[j].entry.Time)
	})

	// Reuse the runtime path's line group so prefixes align and json output mode
	// wraps each line the same way.
	group := d.LineGroup()
	writers := map[string]io.WriteCloser{}
	defer func() {
		for _, w := range writers {
			w.Close()
		}
	}()
	writerFor := func(service, stream string) io.Writer {
		key := service + "/" + stream
		if w, ok := writers[key]; ok {
			return w
		}
		p := ""
		if prefix {
			p = fmt.Sprintf("%-*s | ", width, service)
		}
		dest := d.Out()
		if stream == "stderr" {
			dest = d.Err()
		}
		w := group.Writer(dest, p)
		writers[key] = w
		return w
	}

	for _, t := range all {
		line := t.entry.Body
		if opts.Timestamps {
			line = t.entry.Time.UTC().Format(time.RFC3339Nano) + " " + line
		}
		fmt.Fprintln(writerFor(t.service, entryStream(t.entry)), line)
	}
	return nil
}

// storeTailLimit maps `--tail` onto a row limit. "all" (the default) leaves it
// unset so the server's own cap applies.
func storeTailLimit(tail string) int {
	if tail == "" || tail == "all" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(tail, "%d", &n); err != nil || n <= 0 {
		return 0
	}
	return n
}

// entryStream recovers which stream carried a record.
//
// The recorder stamps cornus.stream on every line it captures. A record without
// it came from the app's own OTLP exporter rather than from container output, and
// stdout is the right destination for those: they are not errors, and routing
// them to stderr would corrupt a pipeline.
func entryStream(e obsstore.LogEntry) string {
	if e.Attributes == "" {
		return "stdout"
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(e.Attributes), &attrs); err != nil {
		return "stdout"
	}
	if s, ok := attrs["cornus.stream"].(string); ok && s == "stderr" {
		return "stderr"
	}
	return "stdout"
}

// storeUnavailableError explains what to turn on, rather than reporting a bare
// 404 the user has no way to act on. Which remedy to name depends on why they
// ended up here: a filter flag pulled them in implicitly, and saying so is the
// difference between a fixable message and a confusing one.
func storeUnavailableError(c *LogsCmd) error {
	if c.storeOnly() {
		return fmt.Errorf("--match/--severity search recorded logs, but %w", client.ErrObsUnavailable)
	}
	return client.ErrObsUnavailable
}

// replicaCount reports how many instances a deployment currently has, for the
// runtime fan-out.
//
// It falls back to 1 on any error rather than failing the command: a status
// lookup that cannot answer should degrade to today's single-stream behavior,
// not deny the user the logs they asked for.
func (r *runtime) replicaCount(ctx context.Context, resource string) int {
	if r.client == nil {
		return 1
	}
	st, err := r.client.Status(ctx, resource)
	if err != nil || len(st.Instances) == 0 {
		return 1
	}
	return len(st.Instances)
}
