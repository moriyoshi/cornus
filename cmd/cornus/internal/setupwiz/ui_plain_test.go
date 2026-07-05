package setupwiz

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
)

var errStub = errors.New("stub")

func plainUIFor(input string) (*plainUI, *bytes.Buffer) {
	var out bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: &out, Stderr: &out, Stdin: strings.NewReader(input), Output: "plain"})
	return newPlainUI(d), &out
}

var threeOpts = []Option{{Label: "a"}, {Label: "b"}, {Label: "c"}}

func TestPlainSelectByNumber(t *testing.T) {
	u, _ := plainUIFor("2\n")
	got, err := u.Select("pick", "", threeOpts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("got index %d, want 1", got)
	}
}

func TestPlainSelectEmptyIsDefault(t *testing.T) {
	u, _ := plainUIFor("\n")
	got, err := u.Select("pick", "", threeOpts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("empty answer should pick the default 2, got %d", got)
	}
}

func TestPlainSelectInvalidReAsks(t *testing.T) {
	u, out := plainUIFor("9\nx\n3\n")
	got, err := u.Select("pick", "", threeOpts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("got %d, want 2 after re-asking", got)
	}
	if !strings.Contains(out.String(), "between 1 and 3") {
		t.Errorf("expected a re-ask hint, got %q", out.String())
	}
}

func TestPlainInputDefaultAndValue(t *testing.T) {
	u, _ := plainUIFor("\n")
	got, err := u.Input(Question{Title: "name", Default: "dflt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "dflt" {
		t.Errorf("empty input should return the default, got %q", got)
	}

	u2, _ := plainUIFor("value\n")
	got, err = u2.Input(Question{Title: "name"})
	if err != nil || got != "value" {
		t.Errorf("Input = %q, %v; want value", got, err)
	}
}

func TestPlainInputValidateReAsks(t *testing.T) {
	u, out := plainUIFor("bad\ngood\n")
	got, err := u.Input(Question{Title: "name", Validate: func(s string) error {
		if s != "good" {
			return errStub
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "good" {
		t.Errorf("got %q, want good", got)
	}
	if !strings.Contains(out.String(), "invalid") {
		t.Errorf("expected an invalid notice, got %q", out.String())
	}
}

func TestPlainConfirm(t *testing.T) {
	for _, tc := range []struct {
		in   string
		def  bool
		want bool
	}{
		{"y\n", false, true},
		{"n\n", true, false},
		{"\n", true, true},
		{"\n", false, false},
	} {
		u, _ := plainUIFor(tc.in)
		got, err := u.Confirm("ok?", tc.def)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("Confirm(%q, def=%v) = %v, want %v", tc.in, tc.def, got, tc.want)
		}
	}
}

func TestPlainEOFAborts(t *testing.T) {
	u, _ := plainUIFor("")
	if _, err := u.Select("pick", "", threeOpts, 0); err != ErrAborted {
		t.Errorf("Select on EOF = %v, want ErrAborted", err)
	}
	u2, _ := plainUIFor("")
	if _, err := u2.Input(Question{Title: "x"}); err != ErrAborted {
		t.Errorf("Input on EOF = %v, want ErrAborted", err)
	}
	u3, _ := plainUIFor("")
	if _, err := u3.Confirm("x?", false); err != ErrAborted {
		t.Errorf("Confirm on EOF = %v, want ErrAborted", err)
	}
}

func TestPlainBackToken(t *testing.T) {
	u, _ := plainUIFor("<\n")
	if _, err := u.Select("pick", "", threeOpts, 0); err != ErrBack {
		t.Errorf("Select '<' = %v, want ErrBack", err)
	}
	u2, _ := plainUIFor("<\n")
	if _, err := u2.Input(Question{Title: "x"}); err != ErrBack {
		t.Errorf("Input '<' = %v, want ErrBack", err)
	}
	u3, _ := plainUIFor("<\n")
	if _, err := u3.Confirm("x?", false); err != ErrBack {
		t.Errorf("Confirm '<' = %v, want ErrBack", err)
	}
}

// TestPlainMultiQuestionBuffering proves the single shared reader carries answers
// across several prompts from one piped write (a fresh reader per prompt would
// swallow the buffered second line).
func TestPlainMultiQuestionBuffering(t *testing.T) {
	u, _ := plainUIFor("first\nsecond\n")
	a, err := u.Input(Question{Title: "1"})
	if err != nil || a != "first" {
		t.Fatalf("first = %q, %v", a, err)
	}
	b, err := u.Input(Question{Title: "2"})
	if err != nil || b != "second" {
		t.Fatalf("second = %q, %v (buffered line lost?)", b, err)
	}
}

func TestPlainInputShowsExample(t *testing.T) {
	u, out := plainUIFor("https://x\n")
	if _, err := u.Input(Question{Title: "Server URL", Example: "https://cornus.example.com", Validate: validateServerURL}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(e.g. https://cornus.example.com)") {
		t.Errorf("plain prompt should show the example: %q", out.String())
	}
}
