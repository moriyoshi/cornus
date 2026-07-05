#!/usr/bin/env bash
#
# Build ONE released cornus binary in the "all-in-one" configuration: the
# embedded OpenTelemetry Collector (`otelcol`) and the built-in observability
# store (`imbh`) both linked in, so a downloaded binary records workload
# telemetry with no flags and no extra build step.
#
# Used by .github/workflows/release.yml for every published binary, and runnable
# locally to reproduce a release artifact. One script rather than five inline
# workflow snippets: the tag list, the version stamp and the "did the store
# actually link?" assertion have to be identical across platforms, and that is
# only cheap to guarantee in one place.
#
# Required env:
#   GOOS, GOARCH   target platform (must be the platform this script RUNS on —
#                  see "Why native" below)
#   OUT            output binary path
# Optional env:
#   VERSION        version stamped into `cornus version` (default: dev)
#   IMBH_VERSION   imbh-go release supplying libimbhgo.a (default: v0.3.0)
#
# Why native, not cross-compiled: `imbh` is a Rust static library reached over
# cgo, so every leg needs CGO_ENABLED=1, a C toolchain, and a libc-matched
# libimbhgo.a. Cross-compiling all of that from one runner would mean three
# cross toolchains and an unrunnable binary we could not verify. Building on the
# target platform instead keeps each leg's toolchain native AND lets us execute
# the result to prove the store linked in.
#
# Linux is special: it is built against MUSL and fully statically linked, so the
# downloaded binary runs on any distro (Alpine included) exactly as the previous
# CGO_ENABLED=0 builds did. Run the linux legs inside a musl environment
# (golang:1.26-alpine + `apk add build-base`); the workflow does this with a
# `docker run`, which also keeps Alpine out of the runner where the JS-based
# actions need glibc. macOS and Windows link dynamically against their platform
# libc, which is normal for both and needs no special base.
set -euo pipefail

: "${GOOS:?GOOS is required}"
: "${GOARCH:?GOARCH is required}"
: "${OUT:?OUT is required}"
VERSION="${VERSION:-dev}"
IMBH_VERSION="${IMBH_VERSION:-v0.3.0}"

# Keep in lockstep with the Dockerfile BUILD_TAGS and the Makefile IMBH_TAGS. The
# `sable_extern_lib` tag tells sable that the embedder supplies the one combined
# archive rather than sable linking its own.
BUILD_TAGS="netgo osusergo otelcol imbh sable_extern_lib"

echo "==> building cornus ${VERSION} for ${GOOS}/${GOARCH} (tags: ${BUILD_TAGS})"

# Fetch the prebuilt libimbhgo.a for THIS platform and pick up the -L/-l it needs.
# The tool resolves the right cell from runtime.GOOS/GOARCH plus a glibc-vs-musl
# probe of the filesystem, which is correct precisely because we build natively.
# It prints `export CGO_LDFLAGS="..."` on stdout and progress on stderr.
echo "==> fetching libimbhgo.a (${IMBH_VERSION})"
eval "$(go run "github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@${IMBH_VERSION}" -print-env)"
if [ -z "${CGO_LDFLAGS:-}" ]; then
  echo "ERROR: imbhgo-fetch did not report CGO_LDFLAGS; no archive for ${GOOS}/${GOARCH}?" >&2
  exit 1
fi

LDFLAGS="-s -w -X main.version=${VERSION}"

if [ "${GOOS}" = "linux" ]; then
  # musl ships no static libgcc_s, but sable's cgo directives ask for -lgcc_s
  # unconditionally on linux (link_extern.go), with no musl variant to select.
  # libgcc_eh.a carries the unwinder symbols the Rust runtime actually needs
  # from it, so point -lgcc_s at that. rt/util/dl resolve to musl's own empty
  # stub archives, so this is the only real gap.
  #
  # Revisit if upstream sable gains a musl-aware link file; until then this shim
  # is what makes a fully static all-in-one binary possible.
  shim="$(mktemp -d)"
  eh="$(find /usr/lib/gcc -name libgcc_eh.a -print -quit 2>/dev/null || true)"
  if [ -z "${eh}" ]; then
    echo "ERROR: libgcc_eh.a not found; install build-base (Alpine) so -lgcc_s can be shimmed" >&2
    exit 1
  fi
  ln -sf "${eh}" "${shim}/libgcc_s.a"
  export CGO_LDFLAGS="${CGO_LDFLAGS} -L${shim}"
  LDFLAGS="${LDFLAGS} -linkmode external -extldflags \"-static -L${shim}\""
fi

CGO_ENABLED=1 GOOS="${GOOS}" GOARCH="${GOARCH}" \
  go build -tags "${BUILD_TAGS}" -ldflags "${LDFLAGS}" -o "${OUT}" ./cmd/cornus

# Refuse to publish an artifact whose observability store silently no-ops. Lives
# in its own script so the assertion can be tested against a stub binary rather
# than only after a full release build; it also derives a runnable path from a
# possibly-relative OUT.
bash "$(dirname "$0")/verify-release-binary.sh" "${OUT}"

case "${OUT}" in
  /* | ./* | *:*) EXE="${OUT}" ;;
  *) EXE="./${OUT}" ;;
esac

if [ "${GOOS}" = "linux" ]; then
  # A dynamically linked "static" build would defeat the point and only surface
  # on a user's Alpine box, so check the artifact itself rather than trusting the
  # linker flags above.
  if command -v file >/dev/null 2>&1 && file "${EXE}" | grep -q 'dynamically linked'; then
    echo "ERROR: ${OUT} is dynamically linked; the linux release binaries must be static" >&2
    exit 1
  fi
fi

echo "==> ok: ${OUT}"
