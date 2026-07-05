package composecli

import (
	"testing"

	"github.com/alecthomas/kong"
)

// TestHostFlagAcceptsServerAlias covers the `aliases='server'` on Cmd.Host.
//
// The alias exists because the documented quick-start tells readers to pass
// `--server`, and every other cornus command accepts it — but `cornus compose`
// spells its endpoint flag `-H/--host`, so before the alias the copy-pasted
// command from the docs failed with "unknown flag: --server". That is a first-run
// failure on the first page a new user reads.
//
// The item was closed with no test, and the note in the audit is precise:
// removing `aliases='server'` left the whole suite green. Nothing else in the
// tree parses that spelling.
//
// Both spellings are asserted, and the equality between them is the actual
// contract — an alias that parses but lands in a different field would satisfy a
// one-sided check.
func TestHostFlagAcceptsServerAlias(t *testing.T) {
	const endpoint = "https://cornus.example:5000"
	for _, spelling := range []string{"--server", "--host", "-H"} {
		t.Run(spelling, func(t *testing.T) {
			var cli struct {
				Compose Cmd `kong:"cmd,name='compose'"`
			}
			parser, err := kong.New(&cli, kong.Name("cornus"))
			if err != nil {
				t.Fatalf("kong.New: %v", err)
			}
			// `ps` is the cheapest subcommand that satisfies the required-command
			// rule; the flag under test is on the group, not the subcommand.
			if _, err := parser.Parse([]string{"compose", spelling, endpoint, "ps"}); err != nil {
				t.Fatalf("parse %s: %v — the documented quick-start passes this spelling, so a parse "+
					"failure here is a first-run failure for a new user", spelling, err)
			}
			if cli.Compose.Host != endpoint {
				t.Errorf("%s %s put %q in Cmd.Host, want %q: the alias must land in the same field as "+
					"--host, not merely be accepted", spelling, endpoint, cli.Compose.Host, endpoint)
			}
		})
	}
}
