# herdr agent-detection manifests (vendored)

Screen-detection manifests taken from [herdr](https://github.com/ogulcancelik/herdr),
a terminal multiplexer with built-in AI-agent state awareness. They describe how to
recognise, from a pane's rendered screen, whether an agent is idle, working, or
blocked waiting for a human.

## Provenance

| | |
|---|---|
| Upstream | https://github.com/ogulcancelik/herdr |
| Commit | `eacea2daf0b72973173b728936b27478374f2cd2` |
| Commit date | 2026-08-03 |
| Vendored from | `src/detect/manifests/*.toml` |
| Vendored on | 2026-08-03 |
| License | Apache License 2.0 (see `LICENSE`) |

**Unmodified.** Every file under `manifests/` is byte-identical to upstream. If any
of them is ever changed, Apache-2.0 section 4(b) requires the modified files to
carry a prominent notice saying so — add it to the file itself, and say so here.

Upstream ships no `NOTICE` file, so Apache-2.0 section 4(d) does not apply. The
manifests carry no per-file copyright headers of their own; attribution lives in
this README and in the retained `LICENSE`, which satisfies 4(a) and 4(c).

## Updating

Re-copy from upstream rather than editing in place, then update the commit and
date above:

```sh
git clone --depth 1 https://github.com/ogulcancelik/herdr /tmp/herdr
cp /tmp/herdr/src/detect/manifests/*.toml third_party/herdr/manifests/
cp /tmp/herdr/LICENSE third_party/herdr/LICENSE
```

Upstream updates these as agent UIs change (they carry their own `version` and
`updated_at` fields), so a refresh is a routine maintenance task, not a one-off.

## Status in Cornus

**Bundled and verified intact; NOT yet used for classification.** Cornus's own
detector (`cmd/cornus/internal/webbff/agentdetect.go`) reads a different and much
simpler schema — see `rules.toml` there — and cannot interpret these files. The
gap is deliberate and tracked: bundling the data is separable from teaching the
engine to read it.

The manifest schema these files use, for whoever does that work:

```toml
id = "claude"
version = "2026.07.13.1"
min_engine_version = 2
updated_at = "2026-07-13T00:00:00Z"
aliases = ["claude-code"]

[[rules]]
id       = "osc_title_working"
state    = "working"          # working | idle | blocked | unknown | done
priority = 800                # higher wins
region   = "osc_title"        # which PART of the screen this rule reads:
                              #   prompt_box_body, after_last_horizontal_rule,
                              #   bottom_non_empty_lines(N), whole_recent,
                              #   osc_title, osc_progress
contains   = ["…"]            # substrings, AND
line_regex = ["^…"]           # anchored at line start
regex      = ["…"]            # unanchored
any = [ { … } ]               # OR gate
all = [ { … } ]               # AND gate
not = [ { … } ]               # negation
visible_working = true        # …/ visible_blocker / visible_idle
skip_state_update = false
```

`region` is the primitive Cornus's own rules lack, and the reason its patterns
misclassify: with one fixed match region, `[y/n]` inside a prompt box cannot be
told apart from the same text in `git log` output. `osc_title` / `osc_progress`
are notable too — upstream treats terminal escape sequences as detection
EVIDENCE, where Cornus currently strips them before classification.
