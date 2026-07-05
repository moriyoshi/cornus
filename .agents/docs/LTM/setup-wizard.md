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

## Container-Install Scenario

The Docker-host flow includes “Docker host (server in a container)”. It bundles
**no file**: the guide prints the `docker run` command directly, with the daemon
socket, the `rshared` data-directory bind, explicit `CORNUS_DATA`, and the server
port matching the saved connection profile. Preflight guidance comes before
startup so a path-mapping failure is reported before workloads silently receive
empty binds.

It emitted a `cornus-compose.yaml` until 2026-07-28. That was dropped because the
arrangement needs only Docker, and the artifact silently added Compose as a
requirement; a shell script was considered and rejected for the same shape of
reason (something to audit before running). `containerRunCommand` /
`containerPreflightCommand` in `guidance.go` build both commands from one
`containerBinds` helper, so the check and the run cannot disagree.

## Backend-Aware Setup Flow

The first wizard decision is whether a Cornus server already exists. When it does
not, the wizard prints the backend-specific setup guide immediately and suppresses
live operations that cannot succeed: ingress probing, SSH-key enrollment, and the
connection test. All five backends appear in the local picker; SSH scenarios add
bare and Incus, and `ssh-docker` asks whether the remote server itself runs in a
container.

`docs/guides/server-setup.md` provides stable explicit anchors for every
arrangement. Wizard references are complete HTTPS URLs and are checked against the
actual Markdown anchors from Go tests. User-facing guidance strings are
considered documentation even though the normal docs gate cannot see them.

The container arrangement generates no artifact: plain `docker run` requires
Docker only, while a Compose file would add an unnecessary prerequisite. Local
bare is the opposite case and offers a systemd unit because Cornus itself is the
workload supervisor when `CORNUS_BARE_SHIM` is off. Bundle a file only when the
arrangement genuinely requires the tool that consumes it.

Guides that affect later questions are printed once, before those questions;
closing output contains next steps rather than a duplicate guide. Parameters used
by generated commands and artifacts come from shared helpers so preflight and run
commands cannot diverge.

The output style uses existing lipgloss/termenv dependencies. The documentation
URL uses one direct SGR underline run because lipgloss's forced-profile underline
renders per character and breaks terminal linkification.
