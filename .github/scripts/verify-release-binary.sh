#!/usr/bin/env bash
#
# Assert that ONE built cornus binary really carries the features a release is
# supposed to ship: the built-in observability store (`imbh`) and the embedded
# OpenTelemetry Collector (`otelcol`).
#
# Split out of build-release-binary.sh so it can be exercised on its own against
# a stub binary (see cmd/cornus/release_verify_test.go) instead of only after a
# multi-minute cross-platform build.
#
# Usage: verify-release-binary.sh <binary-path>
#
# Why assert at all: a mistyped build tag still COMPILES — it just selects the
# stub implementation — and every other signal (exit status, file size, `file`
# output) looks fine, so the only honest check is what the binary reports about
# itself. That is the payoff of building natively: the artifact is runnable on
# the machine that produced it.
set -euo pipefail

BIN="${1:?usage: verify-release-binary.sh <binary-path>}"

# The caller normally passes a relative path (the workflow uploads the artifact
# by that name, and a relative path keeps the Windows leg working under git-bash,
# where an absolute POSIX path would not survive the trip into a native Windows
# `go build`). A bare relative name is not executable, so derive an explicit path.
case "${BIN}" in
  /* | ./* | *:*) EXE="${BIN}" ;;
  *) EXE="./${BIN}" ;;
esac

echo "==> verifying the shipped feature set"

# Run once, then match against the captured report. Do NOT pipe through
# `tee /dev/stderr`: git-bash on the Windows runner cannot open /dev/stderr, so
# tee exited non-zero there and `set -o pipefail` turned that into a bogus
# "does not carry the observability store" failure on a perfectly good binary.
FEATURES="$("${EXE}" version --features --output json)"
printf '%s\n' "${FEATURES}" >&2

# The key names and the yes/no vocabulary are a pipeline contract pinned by
# TestVersionFeaturesJSONContract in cmd/cornus/serve_obs_test.go, including the
# compact (space-free) encoding these patterns rely on.
case "${FEATURES}" in
  *'"obsstore":"yes"'*) ;;
  *)
    echo "ERROR: ${BIN} does not carry the observability store" >&2
    exit 1
    ;;
esac
case "${FEATURES}" in
  *'"otelcollector":"yes"'*) ;;
  *)
    echo "ERROR: ${BIN} does not carry the embedded OpenTelemetry Collector" >&2
    exit 1
    ;;
esac

echo "==> ok: ${BIN} carries the observability store and the embedded collector"
