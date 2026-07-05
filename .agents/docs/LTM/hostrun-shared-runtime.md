# Shared Host Runtime Machinery

## Summary

`pkg/deploy/internal/hostrun` centralizes Linux runtime code shared by the daemonless barehost and containerdhost backends. It removes roughly one thousand lines of duplicated, daemon-agnostic implementation while preserving each backend's distinct runtime client, record source, and lifecycle orchestration.

## Key Facts

- The package is internal to `pkg/deploy`, Linux-only, and links no Moby or BuildKit dependencies.
- Shared slices are OCI spec options, netns liveness, managed hosts files, volumes/image seeding, CNI networking, and Docker-compatible Stats encoding/streaming.
- Backends retain genuine differences: bare records versus container labels for peer discovery, snapshotter/chain-ID acquisition, containerd label decoding during teardown, logging, and reconciliation.

## Details

`SpecOpts` shapes an OCI `specs.Spec` from `api.DeploySpec` and an `oci.Image`; `NetworkNames` and `SpecAliases` support backend-specific glue. `NetnsAlive` uses NSFS magic. `HostsStore` and `SyncHosts` consume backend-provided `HostsPeer` lists so the shared managed-section splice preserves user edits.

`VolumeStore` handles named and anonymous directories, instance mounts, reaping, and `SeedVolumes` copies image content into empty volumes through a snapshotter view. Callers must retain the `len(vols) == 0` short circuit before resolving a snapshotter.

`CNIManager` owns subnet allocation, conflist generation, plugin probing, setup/teardown, network removal, and `Attachment`/`InstanceNetworks`. Constructors take the backend data-dir segment and error prefix. Containerd wraps it only to decode labels for teardown and to preserve its fake-network test seam.

The Stats layer defines `StatsSample`, Docker JSON frame types, `ToDockerStats`, `/proc/net/dev` parsing, host fallbacks, and `StreamStats`. Each backend supplies only a sampler: containerd consumes task metrics, while barehost reads cgroup files directly.

## Files

- `pkg/deploy/internal/hostrun/spec_linux.go`
- `pkg/deploy/internal/hostrun/netns_linux.go`
- `pkg/deploy/internal/hostrun/hosts_linux.go`
- `pkg/deploy/internal/hostrun/volumes_linux.go`
- `pkg/deploy/internal/hostrun/network_linux.go`
- `pkg/deploy/internal/hostrun/stats_linux.go`

## Test Coverage

Unit tests moved with their ownership: spec options, hosts splice behavior, volume lifecycle, CNI helpers, network parsing, stats projection, and stream behavior are tested in `hostrun`. Both consuming backend suites compile against the exported seams; bare E2E exercises the shared stores and CNI against real runc.

## Pitfalls

- Keep the package free of daemon clients and cgroup manager dependencies; that is what preserves barehost's lightweight dependency invariant.
- Do not force superficially similar lifecycle code into hostrun when its recovery or logging semantics differ between backends.
