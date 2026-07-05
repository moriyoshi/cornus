package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLevelFromEnv(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"bogus":   slog.LevelInfo,
	}
	for v, want := range cases {
		t.Setenv("CORNUS_LOG_LEVEL", v)
		if got := LevelFromEnv(); got != want {
			t.Errorf("LevelFromEnv(%q) = %v, want %v", v, got, want)
		}
	}
}

// recordHandler captures records so a test can assert the fan-out reached it.
type recordHandler struct{ records *[]slog.Record }

func (h recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return nil
}
func (h recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordHandler) WithGroup(string) slog.Handler      { return h }

func TestInitWithFansOut(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var got []slog.Record
	InitWith(recordHandler{&got})
	slog.Info("hello", "k", "v")

	if len(got) != 1 {
		t.Fatalf("fanned-out handler received %d records, want 1", len(got))
	}
	if got[0].Message != "hello" {
		t.Errorf("record message = %q, want %q", got[0].Message, "hello")
	}
}

// specAttrs is a LogAttrsProvider stand-in for a domain type describing its own
// log context.
type specAttrs struct{ name string }

func (s specAttrs) LogAttrs() []slog.Attr {
	return []slog.Attr{slog.Group("kubernetes", slog.String("deployment", s.name))}
}

func TestExpand(t *testing.T) {
	got := expand([]any{
		"a", 1,
		slog.String("b", "two"),
		[]slog.Attr{slog.Int("c", 3), slog.Int("d", 4)},
		specAttrs{name: "web"},
		"e", 5,
	})
	want := []string{"a", "b", "c", "d", "kubernetes", "e"}
	if len(got) != len(want) {
		t.Fatalf("expand returned %d attrs (%v), want %d", len(got), got, len(want))
	}
	for i, key := range want {
		if got[i].Key != key {
			t.Errorf("attr[%d].Key = %q, want %q", i, got[i].Key, key)
		}
	}
	// The provider's element must survive as a group, not a flattened !BADKEY.
	if got[4].Value.Kind() != slog.KindGroup {
		t.Errorf("provider attr kind = %v, want Group", got[4].Value.Kind())
	}
}

func TestWithAttrsRoundTrip(t *testing.T) {
	ctx := context.Background()
	if a := attrsFrom(ctx); a != nil {
		t.Fatalf("fresh context carries attrs: %v", a)
	}
	ctx = WithAttrs(ctx, "component", "gc")
	ctx = WithAttrs(ctx, slog.Int("tick", 1))
	got := attrsFrom(ctx)
	if len(got) != 2 || got[0].Key != "component" || got[1].Key != "tick" {
		t.Fatalf("accumulated attrs = %v, want [component tick]", got)
	}
}

// TestFromContextMerge exercises the end-to-end output: context-accumulated
// group identity plus a top-level per-call error key.
func TestFromContextMerge(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx := WithAttrs(context.Background(), specAttrs{name: "web"})
	log := FromContext(ctx)
	log.WarnContext(ctx, "apply failed", "error", "boom")

	out := buf.String()
	if !strings.Contains(out, "kubernetes.deployment=web") {
		t.Errorf("output %q missing grouped identity kubernetes.deployment=web", out)
	}
	if !strings.Contains(out, "error=boom") {
		t.Errorf("output %q missing top-level error=boom", out)
	}
	if strings.Contains(out, "kubernetes.error") {
		t.Errorf("output %q nested error under the group; want top-level", out)
	}
}

func TestFromContextNoAttrsReturnsDefault(t *testing.T) {
	if FromContext(context.Background()) != slog.Default() {
		t.Error("FromContext with no attrs should return slog.Default() unchanged")
	}
}

// traceIDKey is a stand-in for a span-carrying context value.
type traceIDKey struct{}

// TestFromContextEnrichesFromHook verifies FromContext folds SetContextAttrs
// hook attributes (the mechanism OTel trace/span weaving rides on) onto the
// logger alongside the context and call-site attrs.
func TestFromContextEnrichesFromHook(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	t.Cleanup(func() { SetContextAttrs(nil) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	SetContextAttrs(func(ctx context.Context, dst []slog.Attr) []slog.Attr {
		if v, ok := ctx.Value(traceIDKey{}).(string); ok {
			return append(dst, slog.String("trace_id", v))
		}
		return dst
	})

	ctx := context.WithValue(context.Background(), traceIDKey{}, "abc123")
	log := FromContext(ctx, slog.Group("kubernetes", "deployment", "web"))
	log.WarnContext(ctx, "apply failed", "error", "boom")

	out := buf.String()
	for _, want := range []string{"trace_id=abc123", "kubernetes.deployment=web", "error=boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}

	// No span value in context: no trace_id folded on.
	buf.Reset()
	FromContext(context.Background()).WarnContext(context.Background(), "no span")
	if strings.Contains(buf.String(), "trace_id") {
		t.Errorf("output %q added trace_id without a span in context", buf.String())
	}
}

func TestSetContextAttrsNilDisables(t *testing.T) {
	t.Cleanup(func() { SetContextAttrs(nil) })

	SetContextAttrs(func(_ context.Context, dst []slog.Attr) []slog.Attr {
		return append(dst, slog.String("k", "v"))
	})
	SetContextAttrs(nil)
	// With the hook cleared and no other attrs, FromContext returns Default as-is.
	if FromContext(context.Background()) != slog.Default() {
		t.Error("FromContext should return slog.Default() unchanged after SetContextAttrs(nil)")
	}
}
