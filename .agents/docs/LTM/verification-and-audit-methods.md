# Verification and Audit Methods

## Summary

Cornus uses verification as a separate engineering activity rather than treating a
green command as proof by itself. The recurring rule is to identify the property
being claimed, make that property observable, and then demonstrate that the check
fails under a compiling behavioral regression that could realistically occur.

This document consolidates the reusable methods learned during the July 2026 TODO
sweeps, backend field audits, translation checks, CI triage, and external
attestation review.

## Key Facts

- Re-verify every backlog claim and its proposed remedy against the current tree
  before acting. A dated TODO is a hypothesis, not an instruction.
- Before trusting a check, ask what a pass would look like if the claim were
  false. If the answer is plausible, the check needs another control.
- Neutralization must break behavior while still compiling. Prefer mutations a
  real regression would take, not the mutation the test was designed to notice.
- Assertions about absence, silence, low allocation, or bounded work need a
  positive control proving the relevant path actually ran.
- Source-structure checks are proxies. When behavior is reachable, pair structure
  with a value or behavioral assertion.
- A shared helper needs two kinds of coverage: its own correctness and adoption at
  every caller.
- Live E2E tests are needed for contracts that exist only between components, but
  self-skips must be made fail-closed in dedicated CI legs.
- Durable fixes prefer a single source of truth over duplicated literals plus a
  drift test when the dependency direction permits it.

## Details

### Re-grounding a backlog item

The TODO sweeps repeatedly found items that were already fixed, had the wrong
scope, or proposed a remedy contradicted by the code. The operating sequence is:

1. Reproduce or otherwise establish that the observation is still true.
2. Trace the affected behavior to its actual consumer, not only its declaration.
3. Check whether a guard or realization exists in another layer.
4. Evaluate the proposed remedy separately from the observation.
5. Record decisions and declines explicitly; a checked box alone cannot say
   whether work was implemented or deliberately rejected.

Adjacency to recently touched code is cheap context, not prioritization. Re-rank
against the current tree rather than against the backlog's own ordering.

Two measured properties of this backlog, kept because both cost something to
learn. A re-grounding pass fails toward "done", so the rows that CLOSE work
deserve the most scrutiny: a false "still open" costs one re-check, while a false
"done" retires real work and nobody looks again. And a stale entry is worse than
no entry, because it directs effort at closed work and makes the backlog's size a
lie about the remaining work.

### Designing a check that proves its claim

A useful check distinguishes the intended implementation from a plausible wrong
implementation:

- A warning-presence test must count warnings per field. Keying by warning text
  misses differently worded duplicates.
- A silence test needs a positive case showing the warning branch still exists.
- A differential translation probe needs domain-valid values; otherwise input
  validation is indistinguishable from a dropped field.
- A benchmark or allocation ceiling needs an assertion that the measured operation
  retained its result. Doing nothing is exceptionally fast.
- A capability declaration test must inspect the returned capability value, not
  merely the presence of a method declaration.
- A command-line parser test must use the same option constructor as `main()`;
  testing a lookalike parser only proves the replica is self-consistent.
- A timeout test should impose its own outer deadline so its neutralized failure is
  a bounded diagnostic rather than a wedged suite.

For table-driven tests, confirm the intended subtest actually ran. A green
`go test -run ...` result proves nothing if the filter selected a neighboring test
or none of the new rows.

### Neutralization

After the regression test is written:

1. Introduce a compiling behavioral regression.
2. Run the narrow test and confirm it fails for the intended reason.
3. Restore the implementation.
4. Re-run the narrow test and the appropriate package or repository gate.

Deleting a method is a weak neutralization for a guard that parses method
declarations; changing `return true` to `return false` is the realistic regression.
Likewise, removing an import and getting a compile error proves only that the
symbol was referenced.

When several independent regressions are possible, neutralize each important
shape. The remote-mode factory guard, for example, must catch an open-coded env
read, `WithRemote(false)`, and deletion of the option.

### Making properties observable

Several requests to "add a test" required a production seam first:

- `parserOptions()` exposes the real kong option list used by `main()`.
- `containerdhost.LogShimArg` moved out of a Linux-tagged file so both production
  registrations can be compared against one production value.
- `newBridgeProxy` exposes the transport carried by the ingress emulator.
- A variable timeout permits a hanging-server test to exercise cancellation in
  milliseconds.

If a property is awkward to observe, change the code shape so the property is
reachable. Avoid constructing a test-only replica of production configuration.

### Shared rules and duplicated knowledge

The recurring high-value defect shape is one rule expressed in two places whose
divergence is silent. Examples included telemetry relay ports, lazy-9P stream
tags, backend-name classifications, hostnames versus `/etc/hosts`, and hub forward
addresses.

Use this order:

1. Replace both copies with one exported or shared value when dependency direction
   allows it.
2. If a single source is impossible, add a guard over both production values.
3. Test adoption at callers as well as correctness of the shared helper.
4. Do not mechanically unify independent values merely because their present
   literals match.

Lexical searches for phrases such as "keep in sync" generate leads but cannot be
treated as a census. Structural searches for duplicated declarations find a
different set, and ordinary diff review still catches defects neither method can
express.

### Choosing the right test level

- Unit tests are appropriate for pure translation, policy, and state-machine
  behavior.
- An `httptest` server is preferable to a fake client when the property is
  cancellation of an in-flight HTTP request. A fake that refuses immediately
  cannot model a peer that accepts and never answers.
- E2E is required when the contract exists between independently correct
  components, such as auth plus BuildKit push, a Kubernetes Ingress plus its
  controller, or recorder output plus a live query engine.
- A scenario must assert the resulting workload or protocol behavior before
  trusting an announcement string.
- Dedicated E2E legs must fail when prerequisites are missing. A self-skip that
  has never been forced to execute is indistinguishable from a passing test.

### Safe experiments in a shared tree

- Treat a green gate as a statement about an instant while other agents are
  editing the same workspace.
- Do not repair another agent's visibly in-progress file to make an unrelated
  gate green; use a focused gate and rerun the full gate when the tree settles.
- Store temporary work under `.agents-workspace/tmp`, but do not point a tool
  that recursively copies the repository at a temporary directory inside the
  repository. `gremlins` recursively copied its own workdir and consumed roughly
  1.1 TB before being stopped.
- Scratch backups must retain path identity. Basename-only backups collide on
  common names such as `spec_linux.go`.
- Rebuild and check the artifact timestamp before interpreting a negative result;
  stale binaries produced several false diagnoses.

## Files

- `AGENTS.md` - neutralization and test-design rules.
- `.agents/docs/QUALITY_GATE.md` - standard repository verification gate.
- `.agents/docs/TESTING.md` - unit and Starlark E2E workflows.
- `.agents/docs/TODO.md` - evidence-bearing open and closed work.
- `.agents/skills/translate-documents/scripts/translation_state.py` - translation
  freshness state and validation.
- `.agents/skills/assess-doc-quality/scripts/` - documentation gate scripts and
  their focused tests.

## Test Coverage

The methods are represented throughout the repository rather than by one package:
per-field backend warning tables, parser option tests, source-structure guards,
allocation ceilings with correctness controls, hanging-server deadline tests,
translation-tool unit tests, and Starlark scenarios with fail-closed prerequisite
checks.

For documentation tooling, run:

```sh
make test-scripts
```

For Go changes, follow `.agents/docs/QUALITY_GATE.md` and the gate in `AGENTS.md`.
For E2E behavior, run the narrow scenario first and then the relevant target
suite.

## Pitfalls

- Do not count a compile error as a failed behavioral neutralization.
- Do not choose the neutralization solely because the test is shaped to catch it.
- Do not infer realization from a field reference count; whole-spec helpers and
  higher layers often consume the value.
- Do not infer absence from a case-sensitive or phrase-specific grep.
- Do not pipe a gate into another command and then report the pipeline's final
  process status as the gate status.
- Do not regenerate translation freshness digests without reviewing the source
  change they attest.
- Do not treat an advisory tool's exit code as proof that its warning comparison
  logic works; test the comparison function directly.

## August 2026 Audit Lessons

Consolidation is not a correctness oracle: repeated older claims can outvote a later correction. After consolidation, search durable memory for changed symbols and verify the surrounding claims against the current tree. Stale paths and test names are useful sampling signals for nearby stale claims.

A negative test for a conjunction proves one guard only when its fixture trips no other guard first. An echo fixture likewise cannot detect symmetric wire corruption. Use asymmetric or independently specified oracles, assert that work actually occurred, and neutralize realistic one-line regressions.

Put seams at the unobservable production boundary and drive the real caller through them. Testing an extracted helper does not prove the caller still uses it. Browser tests must provide geometry, event coordinates, focus, and visible state because jsdom defaults can make drag, placement, animation, and scrolling checks pass for unrelated reasons.
