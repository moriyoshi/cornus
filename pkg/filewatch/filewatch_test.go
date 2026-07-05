package filewatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fast builds a watcher with short timings so tests coalesce and fire quickly.
func fast(paths []string) *Watcher { return New(paths, 40*time.Millisecond, 10*time.Millisecond) }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWaitFiresOnChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "compose.yaml")
	write(t, f, "a")

	w := fast([]string{f})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Change the file shortly after Wait begins.
	go func() {
		time.Sleep(30 * time.Millisecond)
		write(t, f, "b")
	}()

	if !w.Wait(ctx) {
		t.Fatal("Wait returned false; expected a change to fire")
	}
}

func TestWaitCancels(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "compose.yaml")
	write(t, f, "a")

	w := fast([]string{f})
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	if w.Wait(ctx) {
		t.Fatal("Wait returned true on cancellation; expected false")
	}
}

// TestWaitCoalescesBurst verifies a multi-file burst fires a single Wait, and
// the following Wait then reports only later changes.
func TestWaitCoalescesBurst(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "compose.yaml")
	b := filepath.Join(dir, ".env")
	write(t, a, "1")
	write(t, b, "1")

	w := fast([]string{a, b})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		time.Sleep(20 * time.Millisecond)
		write(t, a, "2")
		time.Sleep(5 * time.Millisecond)
		write(t, b, "2")
	}()

	if !w.Wait(ctx) {
		t.Fatal("first Wait: expected change")
	}

	// No further changes: the next Wait must block until ctx times out (returns
	// false), proving the burst was consumed and did not leave the baseline stale.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel2()
	if w.Wait(ctx2) {
		t.Fatal("second Wait fired without a new change (stale baseline?)")
	}
}

// TestWaitDetectsCreate verifies that creating a previously-absent watched file
// (e.g. a .env added later) fires.
func TestWaitDetectsCreate(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "compose.yaml")
	absent := filepath.Join(dir, ".env")
	write(t, present, "x")

	w := fast([]string{present, absent})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		time.Sleep(30 * time.Millisecond)
		write(t, absent, "IMG=nginx\n")
	}()

	if !w.Wait(ctx) {
		t.Fatal("expected creating the absent watched file to fire")
	}
}

func TestNormalizeDedupesAndAbsolutizes(t *testing.T) {
	dir := t.TempDir()
	rel := "compose.yaml"
	abs := filepath.Join(dir, rel)

	// Two spellings of the same absolute path plus a distinct one.
	got := Normalize([]string{abs, filepath.Join(dir, ".", rel), filepath.Join(dir, ".env")})
	if len(got) != 2 {
		t.Fatalf("expected 2 unique paths, got %v", got)
	}
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Errorf("path not absolute: %s", p)
		}
	}
}
