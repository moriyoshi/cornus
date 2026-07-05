package cliout

import (
	"cornus/pkg/build/buildprog"
)

// BuildProgress returns a build-progress sink plus a finish func the caller must
// invoke once the build ends (defer it). It is an append-only renderer writing
// straight to stderr in every mode — colored fancy text on a terminal, plain
// deterministic text otherwise, or NDJSON in json mode — and finish is a no-op.
//
// Build output is deliberately NOT driven through the live bubbletea Progress
// (as an earlier revision did in fancy+TTY mode). That path funneled every RUN
// log line and step transition through tea.Println, which the program silently
// drops whenever its render loop has not started or has already been torn down.
// On common terminals (e.g. TERM=xterm) bubbletea stalls its startup on a
// background-color/cursor-position query, and a fast or early-failing build then
// reaches finish() before anything is flushed — so the whole build emitted no
// output at all. Writing directly to stderr has no such race and keeps
// scripted/piped/json output byte-for-byte identical (the E2E harness greps it).
func (d *Driver) BuildProgress() (buildprog.Sink, func()) {
	switch d.mode {
	case ModeFancy:
		return buildprog.NewSink(d.err, buildprog.Fancy, d.color), func() {}
	case ModeJSON:
		return buildprog.NewSink(d.err, buildprog.JSON, false), func() {}
	default:
		return buildprog.NewSink(d.err, buildprog.Plain, false), func() {}
	}
}
