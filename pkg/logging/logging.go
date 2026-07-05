// Package logging configures cornus's process-wide slog.Default. It is
// deliberately dependency-light (no OpenTelemetry) so every cornus binary —
// including the lightweight cornus-e2e runner and client-side subcommands — can share
// one log setup. The server and caretaker layer OTel log export on top via
// pkg/observability, which fans additional handlers in through InitWith.
package logging

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Init installs a human-readable text slog.Default writing to stderr, with the
// level taken from CORNUS_LOG_LEVEL (debug/info/warn/error; default info).
func Init() { InitWith() }

// InitWith installs the stderr text handler as slog.Default, fanned out to any
// extra handlers as well (e.g. an OpenTelemetry log bridge). Passing no extras
// is exactly Init.
func InitWith(extra ...slog.Handler) {
	stderr := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: LevelFromEnv()})
	var h slog.Handler = stderr
	if len(extra) > 0 {
		h = fanout(append([]slog.Handler{stderr}, extra...))
	}
	slog.SetDefault(slog.New(h))
}

// ContextAttrHook derives extra attributes from ctx and appends them onto dst —
// visitor-style, so it appends in place rather than allocating its own slice —
// returning the extended slice. It is a hook rather than a direct call so this
// package stays free of any OpenTelemetry dependency; pkg/observability installs
// the real extractor (e.g. trace/span ids). FromContext consults it.
type ContextAttrHook func(ctx context.Context, dst []slog.Attr) []slog.Attr

// contextAttrs is the optional ContextAttrHook.
var contextAttrs atomic.Pointer[ContextAttrHook]

// SetContextAttrs installs the hook that folds context-derived attributes — such
// as OpenTelemetry trace_id and span_id — onto every logger FromContext returns.
// Passing nil disables it. Safe to call concurrently with logging; typically
// called once from pkg/observability.Setup.
func SetContextAttrs(hook ContextAttrHook) {
	if hook == nil {
		contextAttrs.Store(nil)
		return
	}
	contextAttrs.Store(&hook)
}

// LogAttrsProvider lets a domain value describe its own log context, so callers
// pass the value itself (e.g. a DeploySpec or a container) to FromContext /
// WithAttrs instead of re-listing its attributes at every call site.
type LogAttrsProvider interface {
	LogAttrs() []slog.Attr
}

// ctxAttrsKey is the single context key under which the accumulated attributes
// live, so FromContext resolves them in one context.Value lookup regardless of
// how many attributes were added along the way.
type ctxAttrsKey struct{}

// WithAttrs returns a context carrying attrs (merged after any already present)
// for later FromContext calls. Each element may be a normal slog argument (a
// slog.Attr, or a "key", value pair), a []slog.Attr, or a value implementing
// LogAttrsProvider. Upstream layers use this to attach scope (component,
// deployment, request id) that inner subsystems then pick up for free.
func WithAttrs(ctx context.Context, attrs ...any) context.Context {
	add := expand(attrs)
	if len(add) == 0 {
		return ctx
	}
	prev := attrsFrom(ctx)
	merged := make([]slog.Attr, 0, len(prev)+len(add))
	merged = append(merged, prev...)
	merged = append(merged, add...)
	return context.WithValue(ctx, ctxAttrsKey{}, merged)
}

// FromContext returns slog.Default() with the context-accumulated attributes and
// the call-site attrs merged on, then any attributes appended by the
// SetContextAttrs hook (e.g. trace/span ids). It is the single entry point for
// obtaining a scoped logger; emit through the *Context methods so ctx reaches
// the handler (and the OTel bridge):
//
//	log := logging.FromContext(ctx, spec) // spec implements LogAttrsProvider
//	log.WarnContext(ctx, "apply failed", "error", err)
//
// Hoist the returned logger once per scope (above any loop) rather than calling
// FromContext per log line, so the context.Value lookup is paid once.
func FromContext(ctx context.Context, attrs ...any) *slog.Logger {
	logger := slog.Default()
	ctxAttrs := attrsFrom(ctx)
	call := expand(attrs)
	hook := contextAttrs.Load()
	if len(ctxAttrs) == 0 && len(call) == 0 && hook == nil {
		return logger
	}
	acc := make([]slog.Attr, 0, len(ctxAttrs)+len(call))
	acc = append(acc, ctxAttrs...)
	acc = append(acc, call...)
	if hook != nil {
		acc = (*hook)(ctx, acc)
	}
	if len(acc) == 0 {
		return logger
	}
	// Attrs are already resolved, so bypass logger.With's argsToAttrs boxing.
	return slog.New(logger.Handler().WithAttrs(acc))
}

// attrsFrom returns the attributes accumulated on ctx, or nil.
func attrsFrom(ctx context.Context) []slog.Attr {
	if a, ok := ctx.Value(ctxAttrsKey{}).([]slog.Attr); ok {
		return a
	}
	return nil
}

// expand normalizes FromContext/WithAttrs variadic arguments into a flat
// []slog.Attr, resolving []slog.Attr and LogAttrsProvider elements and pairing
// the remaining plain arguments exactly as slog does. Resolving providers up
// front keeps a bare non-string, non-Attr value from reaching slog as a !BADKEY.
func expand(attrs []any) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]slog.Attr, 0, len(attrs))
	var plain []any
	flush := func() {
		if len(plain) > 0 {
			r := slog.NewRecord(time.Time{}, 0, "", 0)
			r.Add(plain...)
			r.Attrs(func(a slog.Attr) bool {
				out = append(out, a)
				return true
			})
			plain = plain[:0]
		}
	}
	for _, a := range attrs {
		switch v := a.(type) {
		case slog.Attr:
			flush()
			out = append(out, v)
		case []slog.Attr:
			flush()
			out = append(out, v...)
		case LogAttrsProvider:
			flush()
			out = append(out, v.LogAttrs()...)
		default:
			plain = append(plain, v)
		}
	}
	flush()
	return out
}

// LevelFromEnv resolves the log level from CORNUS_LOG_LEVEL, defaulting to
// Info for any unset or unrecognized value.
func LevelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORNUS_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// fanout dispatches each record to several handlers (e.g. stderr + OTel).
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			// Clone so a handler that mutates the record cannot affect the others.
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
