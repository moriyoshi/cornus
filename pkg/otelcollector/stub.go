//go:build !otelcol

package otelcollector

import "context"

// Compiled reports that the Collector is NOT linked into this build (built
// without the `otelcol` tag).
func Compiled() bool { return false }

// Run returns ErrNotCompiled: the Collector was not linked into this build. The
// signature matches the real implementation so callers compile unconditionally;
// the caretaker surfaces the error rather than silently doing nothing.
func Run(context.Context, Config) error { return ErrNotCompiled }
