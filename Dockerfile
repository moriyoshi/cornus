# syntax=docker/dockerfile:1
#
# cornus: all-in-one container registry, build engine, and deploy engine.
#
# Multi-arch: build with
#   docker buildx build --platform linux/amd64,linux/arm64 -t cornus:latest .
#
# The in-process build engine runs runc + overlayfs, so a cornus container
# that performs builds needs either --privileged or the rootless prerequisites
# (see README "Privilege posture").

# Web UI build: the SolidJS app in web/ compiles to static assets that the Go
# build embeds (pkg/webui //go:embed dist), so this stage must run first. Pinned
# to the BUILD platform: the output is architecture-independent JS/CSS, so a
# multi-arch build compiles it once natively (never emulated npm under QEMU) and
# COPYs the same assets into every target arch's Go stage.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS webui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
# BuildKit cache mounts persist the downloaded module cache (/go/pkg/mod) and the
# compiler's build cache (/root/.cache/go-build) across builds so `go mod
# download`/`go build`/`go run` reuse artifacts instead of refetching and
# recompiling from scratch. The module cache is architecture-independent, so a
# multi-arch build shares one `gomod` mount; the build cache is keyed per
# TARGETARCH so the concurrent amd64/arm64 legs don't contend on one locked mount.
COPY go.mod go.sum ./
COPY third_party/ ./third_party/
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    go mod download
COPY . .
COPY --from=webui /src/pkg/webui/dist/ pkg/webui/dist/
ARG TARGETOS=linux
ARG TARGETARCH
# Release version stamped into `cornus version` (see cmd/cornus/version.go).
# The release workflow passes VERSION=<semver>; the default keeps local
# builds reporting "dev", same as before.
ARG VERSION=dev
# BUILD_TAGS controls which optional features are compiled into the release
# binary, and the default is "everything a user expects out of the box":
#   * `otelcol` embeds the OpenTelemetry Collector so the caretaker sidecar (this
#     same image) can run the workload-telemetry agent.
#   * `imbh` + `sable_extern_lib` embed the built-in observability store, so
#     `cornus serve` records workload logs/traces/metrics with no extra flags
#     (--obs defaults to on wherever the store is linked in; see
#     cmd/cornus/serve.go resolveObsEnabled).
# Drop either to build a leaner image; a binary without them reports the feature
# as not compiled in rather than silently recording nothing.
#
# `imbh` is why this stage is CGO_ENABLED=1: the store is a Rust static library
# (libimbhgo.a) reached over cgo, so it needs a C toolchain, the companion
# `sable_extern_lib` tag telling sable the embedder supplies the combined
# archive, and a CGO_LDFLAGS pointing at a LIBC-MATCHED copy of it. That match is
# why the archive is fetched here rather than cross-compiled in: this stage is not
# pinned to $BUILDPLATFORM, so each arch's leg runs as its own architecture and
# imbhgo-fetch resolves the right cell (glibc, since both this stage and the final
# debian:bookworm-slim are Debian 12) with a native gcc to link it.
#
# The resulting binary is dynamically linked against glibc — deliberately, and
# only safe because it never leaves this image: the caretaker sidecar IS this
# image, the bare-host shim re-execs itself in place, and builderctr's self-built
# image matches the host distribution on purpose (see selfimage.go). The RELEASED
# STANDALONE BINARIES are different — those are fully static musl builds, because
# "runs on any distro" is exactly their job. See .github/workflows/release.yml.
#
# Keep IMBH_VERSION in lockstep with the Makefile's IMBH_VERSION and the imbh-go
# version in go.mod: the archive and the Go bindings share one C ABI.
ARG IMBH_VERSION=v0.1.0
ARG BUILD_TAGS="netgo osusergo otelcol imbh sable_extern_lib"
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild-${TARGETARCH} \
    --mount=type=cache,target=/root/.cache/imbhgo,id=imbhgo-${TARGETARCH} \
    set -eu; \
    eval "$(go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@${IMBH_VERSION} -print-env)"; \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -tags "${BUILD_TAGS}" -ldflags "-s -w -X main.version=${VERSION}" -o /out/cornus ./cmd/cornus; \
    # Fail the build rather than ship an image whose store silently no-ops. A
    # mistyped tag still compiles — it just selects the stub — and every other
    # check here would pass, so assert against what the binary REPORTS about
    # itself. Safe to execute: this stage runs as the target architecture.
    /out/cornus version --features --output json | tee /dev/stderr | grep -q '"obsstore":"yes"' \
      || { echo "BUILD_TAGS=${BUILD_TAGS} did not produce a store-carrying binary" >&2; exit 1; }

# Third-party attribution bundle: license texts (and, for reciprocal licenses
# like MPL-2.0, sources) of every module linked into the binary, plus a CSV
# manifest. Shipped in the final image under /usr/share/doc/cornus/. go-licenses
# must run under the same Go toolchain that resolves the module (see Makefile
# third-party-licenses target); in this stage golang:1.26 guarantees that.
ARG GO_LICENSES_VERSION=v1.6.0
# github.com/rootless-containers/proto/go-proto ships no LICENSE file inside its
# module zip: the Apache-2.0 COPYING lives in the parent repo dir, outside the
# go-proto submodule that Go publishes, so go-licenses cannot locate a license
# file and aborts the whole `save`. It is --ignore'd here and its Apache-2.0
# notice re-injected in the next step, so the shipped attribution bundle stays
# complete. (The module's per-file headers carry the Apache-2.0 grant.)
# The tag list MUST match BUILD_TAGS above. go-licenses reports what the given
# build configuration actually links, so a shorter list here silently omits real
# dependencies: without `imbh`/`sable_extern_lib` the store's Go bindings compile
# as stubs and neither imbh-go nor sable appears in the bundle at all. CGO_ENABLED=1
# for the same reason — the cgo files are what pull those modules in.
#
# KNOWN GAP: go-licenses walks GO modules only, so the Rust crate tree statically
# linked inside libimbhgo.a is NOT covered by this bundle. See
# .agents/docs/TODO.md; closing it needs an upstream notices manifest published
# alongside the archive.
ARG LICENSE_TAGS=netgo,osusergo,otelcol,imbh,sable_extern_lib
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild-${TARGETARCH} \
    --mount=type=cache,target=/root/.cache/imbhgo,id=imbhgo-${TARGETARCH} \
    set -eu; \
    eval "$(go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@${IMBH_VERSION} -print-env)"; \
    export CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-tags=${LICENSE_TAGS}; \
    go run github.com/google/go-licenses@${GO_LICENSES_VERSION} \
        save ./cmd/cornus --save_path=/out/third-party-licenses \
        --ignore cornus \
        --ignore github.com/rootless-containers/proto/go-proto; \
    go run github.com/google/go-licenses@${GO_LICENSES_VERSION} \
        report ./cmd/cornus \
        --ignore cornus \
        --ignore github.com/rootless-containers/proto/go-proto \
        > /out/third-party-licenses/THIRD_PARTY_LICENSES.csv
# Re-inject go-proto's Apache-2.0 attribution (see --ignore rationale above). The
# license body is the canonical Apache-2.0 text, reused verbatim from this repo's
# own LICENSE; the header records go-proto's own copyright holder.
RUN GP=/out/third-party-licenses/github.com/rootless-containers/proto/go-proto \
    && mkdir -p "$GP" \
    && { printf 'rootlesscontainers-proto (github.com/rootless-containers/proto/go-proto)\nCopyright (C) 2018 Rootless Containers Authors\n\nLicensed under the Apache License, Version 2.0; the full license text follows.\n\n'; cat LICENSE; } > "$GP/LICENSE" \
    && printf 'github.com/rootless-containers/proto/go-proto,https://github.com/rootless-containers/proto/blob/f6ee952d53d9/COPYING,Apache-2.0\n' \
        >> /out/third-party-licenses/THIRD_PARTY_LICENSES.csv

FROM debian:bookworm-slim
# runc is the OCI executor the in-process build engine invokes; uidmap +
# rootlesskit + slirp4netns enable the rootless build path on restrictive hosts.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        runc ca-certificates uidmap \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/cornus /usr/local/bin/cornus
# License and third-party attribution notices (Apache-2.0 section 4).
COPY LICENSE NOTICE /usr/share/doc/cornus/
COPY --from=build /out/third-party-licenses /usr/share/doc/cornus/third-party-licenses

ENV CORNUS_DATA=/var/lib/cornus
VOLUME /var/lib/cornus
EXPOSE 5000

ENTRYPOINT ["cornus"]
CMD ["serve", "--addr", ":5000"]
