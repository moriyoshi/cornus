# Interactive Setup Wizard

## Summary

`cornus setup` creates or updates connection profiles through an interactive plain or Bubble Tea UI. It gathers scenario-specific connection details, discovers optional server capabilities, writes one atomic context, and makes revisiting answers predictable rather than lossy.

## Key Facts

- The wizard uses a flat `Answers -> BuildContext -> clientconfig.Context` mapping with `Discover` and `Verify` seams for hermetic tests.
- Rich UI supports Esc and Ctrl-D to go back; plain UI uses the explicit `<` token and treats EOF as abort.
- Previous non-secret answers become defaults when revisiting a question; secrets are never re-displayed.
- The Kubernetes flow probes ingress information and can configure SOCKS5 conduit ingress mode.

## Details

Question order is intentionally stable because scripted tests encode it. Back navigation captures the original current context before the interaction begins, avoiding a context-change round trip that would skip a required confirmation. Discovery belongs inside the namespace step so it reruns only after a meaningful namespace submission.

The rich UI renders compact key legends with color only on the key glyphs, supports arrow/j/k and Ctrl-P/Ctrl-N selection, and combines `Question.Example` with placeholders: examples are presentation hints, while `Default` remains the value used by an empty submission. Plain output presents examples as `(e.g. ...)`.

The ingress probe proposes native mode when a controller is advertised, emulate mode when ingress domain/class information exists without a controller, and off otherwise. Enabling ingress selects SOCKS5 and persists its configuration with the profile.

## Files

- `cmd/cornus/internal/setupwiz/` - wizard flow, plain/rich UI, discovery, and tests.
- `cmd/cornus/setup.go` - CLI binding.
- `docs/cli/setup.md` - English user documentation.

## Test Coverage

Flow tests cover backtracking, cancellation, default retention, scenario selection, wizard-to-context mapping, and ingress probe defaults. Bubble Tea model tests cover keys, legends, examples, and cursor movement.

## Pitfalls

- Adding a step can make queued scripted UI tests semantically wrong while still green; update every affected response sequence.
- Do not reinterpret terminal EOF as back in plain mode: it is indistinguishable from exhausted piped input.
