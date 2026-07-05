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
# binary. The `otelcol` tag embeds the OpenTelemetry Collector so the caretaker
# sidecar (this same image) can run the workload-telemetry agent; drop it to
# build a leaner image without embedded telemetry (the caretaker then reports the
# collector as not compiled in if a telemetry deploy targets it).
ARG BUILD_TAGS="netgo osusergo otelcol"
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild-${TARGETARCH} \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -tags "${BUILD_TAGS}" -ldflags "-s -w -X main.version=${VERSION}" -o /out/cornus ./cmd/cornus

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
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild-${TARGETARCH} \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-tags=netgo,osusergo,otelcol \
        go run github.com/google/go-licenses@${GO_LICENSES_VERSION} \
        save ./cmd/cornus --save_path=/out/third-party-licenses \
        --ignore cornus \
        --ignore github.com/rootless-containers/proto/go-proto \
    && GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-tags=netgo,osusergo,otelcol \
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
