package cliout

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// newTest builds a Driver wired to buffers with an explicit mode, bypassing TTY
// detection so tests are deterministic without a PTY.
func newTest(mode Mode, color bool) (*Driver, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errb := &bytes.Buffer{}
	d := &Driver{
		out:   out,
		err:   errb,
		in:    strings.NewReader(""),
		mode:  mode,
		color: color,
		r:     rendererFor(mode, color),
	}
	return d, out, errb
}

func assertNoANSI(t *testing.T, name string, b *bytes.Buffer) {
	t.Helper()
	if bytes.IndexByte(b.Bytes(), 0x1b) >= 0 {
		t.Errorf("%s: plain output contains an ANSI escape byte: %q", name, b.String())
	}
}

func TestPlainNoANSI(t *testing.T) {
	d, out, errb := newTest(ModePlain, false)
	d.Step("building web")
	d.Done("built web")
	d.Success("all good")
	d.Info("note")
	d.Warn("careful")
	d.Error("bad")
	d.Item("v1.2.3")
	d.Table("A", "B").Row("1", "2").Flush()
	d.KV().Add("servers", "1").Flush()
	assertNoANSI(t, "stdout", out)
	assertNoANSI(t, "stderr", errb)
}

func TestChannelRouting(t *testing.T) {
	d, out, errb := newTest(ModePlain, false)
	// Results -> stdout.
	d.Item("value")
	d.Table("H").Row("r").Flush()
	d.KV().Add("k", "v").Flush()
	// Notices -> stderr.
	d.Step("step")
	d.Done("done")
	d.Warn("warn")
	d.Error("err")

	if !strings.Contains(out.String(), "value") || !strings.Contains(out.String(), "r") || !strings.Contains(out.String(), "k:") {
		t.Errorf("results missing from stdout: %q", out.String())
	}
	if strings.Contains(out.String(), "step") || strings.Contains(out.String(), "warn") {
		t.Errorf("notices leaked into stdout: %q", out.String())
	}
	for _, want := range []string{"step", "done", "warning: warn", "error: err"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr missing %q; got %q", want, errb.String())
		}
	}
}

func TestPlainNoticeFormat(t *testing.T) {
	d, _, errb := newTest(ModePlain, false)
	d.Warn("disk low")
	d.Error("boom")
	d.Info("fyi")
	got := errb.String()
	for _, want := range []string{"warning: disk low\n", "error: boom\n", "fyi\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestPlainTableGolden(t *testing.T) {
	d, out, _ := newTest(ModePlain, false)
	d.Table("CURRENT", "NAME", "SERVER").
		Row("*", "prod", "https://prod:8443").
		Row("", "dev", "http://localhost:5000").
		Flush()
	// tabwriter with minwidth 0, tabwidth 2, padding 2, space padchar.
	want := "" +
		"CURRENT  NAME  SERVER\n" +
		"*        prod  https://prod:8443\n" +
		"         dev   http://localhost:5000\n"
	if out.String() != want {
		t.Errorf("table mismatch:\n got %q\nwant %q", out.String(), want)
	}
}

func TestFancyCarriesSameContentAsPlain(t *testing.T) {
	// Fancy mode now styles output with lipgloss (colored symbols, a header-
	// underline table), so it is no longer byte-identical to plain after merely
	// stripping ANSI and two glyphs. The durable invariant is weaker but real:
	// once ANSI is stripped, every datum plain prints is still present in fancy.
	fd, fout, ferr := newTest(ModeFancy, true)
	fd.Step("building web")
	fd.Warn("careful")
	fd.Error("bad")
	fd.Table("A", "B").Row("1", "2").Flush()
	fd.KV().Add("k", "v").Flush()

	// Color must actually be emitted on stderr in fancy mode.
	if bytes.IndexByte(ferr.Bytes(), 0x1b) < 0 {
		t.Errorf("fancy stderr should contain ANSI color, got %q", ferr.String())
	}
	// After stripping ANSI, the notice messages survive verbatim.
	errText := stripANSI(ferr.String())
	for _, want := range []string{"building web", "careful", "bad"} {
		if !strings.Contains(errText, want) {
			t.Errorf("fancy notice text missing %q in %q", want, errText)
		}
	}
	// The table's headers and every cell survive, aligned into a header-underline
	// layout (a row of ─ appears under the header).
	outText := stripANSI(fout.String())
	for _, want := range []string{"A", "B", "1", "2", "k:", "v", "─"} {
		if !strings.Contains(outText, want) {
			t.Errorf("fancy result text missing %q in %q", want, outText)
		}
	}
}

func TestFancyNoColorHasLayoutNoANSI(t *testing.T) {
	// Fancy without color (e.g. NO_COLOR, or --no-color --output=fancy) keeps the
	// layout — glyphs, the header underline — but emits no ANSI escapes.
	fd, fout, ferr := newTest(ModeFancy, false)
	fd.Step("building web")
	fd.Table("A", "B").Row("1", "2").Flush()
	assertNoANSI(t, "fancy-nocolor stdout", fout)
	assertNoANSI(t, "fancy-nocolor stderr", ferr)
	if !strings.Contains(ferr.String(), symStep) {
		t.Errorf("fancy-nocolor notice should keep its glyph, got %q", ferr.String())
	}
	if !strings.Contains(fout.String(), "─") {
		t.Errorf("fancy-nocolor table should keep the header rule, got %q", fout.String())
	}
}

type deployResult struct {
	Event   string `json:"event"`
	Name    string `json:"name"`
	Running int    `json:"running"`
	Total   int    `json:"total"`
}

func (r deployResult) Human(p Printer) {
	p.Line("deployed %s: %d/%d instances running", r.Name, r.Running, r.Total)
}

func TestEmitHumanAndJSON(t *testing.T) {
	// Human path.
	d, out, _ := newTest(ModePlain, false)
	if err := d.Emit(deployResult{"deployed", "app", 2, 3}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "deployed app: 2/3 instances running\n" {
		t.Errorf("human emit: got %q", out.String())
	}

	// JSON path.
	jd, jout, _ := newTest(ModeJSON, false)
	if err := jd.Emit(deployResult{"deployed", "app", 2, 3}); err != nil {
		t.Fatal(err)
	}
	var got deployResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(jout.String())), &got); err != nil {
		t.Fatalf("json emit not valid: %v (%q)", err, jout.String())
	}
	if got != (deployResult{"deployed", "app", 2, 3}) {
		t.Errorf("json roundtrip mismatch: %+v", got)
	}
}

func TestJSONModeNDJSON(t *testing.T) {
	d, out, errb := newTest(ModeJSON, false)
	d.Item("v1.2.3")                        // stdout: {"value":"v1.2.3"}
	d.Table("A", "B").Row("1", "2").Flush() // stdout: one object per row
	d.Warn("careful")                       // stderr: {"level":"warning",...}

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("stdout line not valid JSON: %q (%v)", line, err)
		}
	}
	var notice map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(errb.String())), &notice); err != nil {
		t.Fatalf("notice not JSON: %v", err)
	}
	if notice["level"] != "warning" || notice["msg"] != "careful" {
		t.Errorf("unexpected notice object: %+v", notice)
	}
}

func TestConfirmGating(t *testing.T) {
	// Not a TTY: returns the default without consulting Prompt.
	d, _, _ := newTest(ModePlain, false)
	d.inTTY = false
	called := false
	d.Prompt = func(string, bool) bool { called = true; return true }
	if got := d.Confirm("set default?", false); got != false || called {
		t.Errorf("non-tty Confirm: got=%v called=%v; want default false, not called", got, called)
	}

	// A TTY: delegates to Prompt.
	d.inTTY = true
	d.Prompt = func(q string, def bool) bool { return true }
	if !d.Confirm("set default?", false) {
		t.Errorf("tty Confirm should delegate to Prompt")
	}
}

func TestLineWriterConcurrency(t *testing.T) {
	d, out, _ := newTest(ModePlain, false)
	const goroutines, lines = 8, 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		prefix := "svc" + string(rune('A'+g)) + " | "
		go func() {
			defer wg.Done()
			w := d.LineWriter(d.Out(), prefix)
			defer w.Close()
			for i := 0; i < lines; i++ {
				// Write in two chunks to exercise partial-line buffering.
				w.Write([]byte("hello "))
				w.Write([]byte("world\n"))
			}
		}()
	}
	wg.Wait()

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(got) != goroutines*lines {
		t.Fatalf("line count: got %d want %d", len(got), goroutines*lines)
	}
	for _, l := range got {
		if !strings.HasSuffix(l, "hello world") || !strings.Contains(l, " | ") {
			t.Fatalf("interleaved or malformed line: %q", l)
		}
	}
}

func TestLineWriterTrailingFlush(t *testing.T) {
	d, out, _ := newTest(ModePlain, false)
	w := d.LineWriter(d.Out(), "p: ")
	w.Write([]byte("no newline here"))
	if out.Len() != 0 {
		t.Errorf("partial line emitted before Close: %q", out.String())
	}
	w.Close()
	if out.String() != "p: no newline here" {
		t.Errorf("trailing flush: got %q", out.String())
	}
}

func TestLineWriterJSON(t *testing.T) {
	d, out, _ := newTest(ModeJSON, false)
	w := d.LineWriter(d.Out(), "web | ")
	w.Write([]byte("starting up\n"))
	w.Close()
	var v map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &v); err != nil {
		t.Fatalf("json log line invalid: %v (%q)", err, out.String())
	}
	if v["type"] != "log" || v["tag"] != "web" || v["line"] != "starting up\n" {
		t.Errorf("unexpected json log object: %+v", v)
	}
}
