# Registry and Storage Backends

## Summary

The registry persists through a pluggable `internal/storage` package: a minimal `ObjectStore` interface backed by filesystem (default), gocloud blob (`mem://`, `s3://`), and build-tag-gated `gs://` / `azblob://` drivers, with all CAS/manifest/tag semantics living once in a shared `Backend` layer. The S3 backend implements native multipart upload for resumable OCI uploads (O(<5 MiB) staging via a JSON sidecar), validated end to end against the winterbaume S3 mock (v0.2.5+ required). The GCS and Azure drivers are validated against local emulators (fake-gcs-server, Azurite) with zero code changes. Registry wire-protocol edge cases and the full registry-over-S3/GCS/Azure paths are covered by unprivileged E2E scenarios.

## Key Facts

- Backend selected at runtime via `cornus serve --storage <ref>`, env `CORNUS_STORAGE`, or the `StorageURL` config field; default is the filesystem layout under `DataDir` (on-disk key layout unchanged, so existing data is compatible).
- `ObjectStore` interface is just `Get` / `Put` / `Stat` / `Delete` / `List` / `Close` (all `ctx`-threaded); adding a backend is ~5 small methods. Registry semantics (sha256 CAS, digest verification, resumable upload staging, manifest/tag/repo indexing) live once in `internal/storage/cas.go`.
- Key layout: `blobs/sha256/<aa>/<hex>`, `repos/<repo>/manifests/<hex>` (value = media type), `repos/<repo>/tags/<tag>` (value = digest). `Repos` recurses `List` because repo names may contain slashes.
- S3 refs take query params `endpoint`, `path_style`, `region`, `access_key`, `secret_key`; the client is built explicitly with `BaseEndpoint` + `UsePathStyle` + static creds for S3-compatible servers.
- Optional `NativeUploader` capability: filesystem commits by renaming the session file (no copy); S3 (`internal/storage/s3.go`) streams to S3 multipart; other gocloud backends fall back to local staging (temp file, streamed on commit).
- `gs://` / `azblob://` require building with `-tags cloudblob`; the default build returns a clear "rebuild with -tags cloudblob" error (before this gating they silently failed with "no driver registered" — the drivers were never blank-imported).
- Both cloudblob drivers pass their gated round-trip tests against local emulators with ZERO code changes: fake-gcs-server (gocloud honors `STORAGE_EMULATOR_HOST`) and Azurite (needs `--skipApiVersionCheck` for the SDK's 2026-06-06 API version — an emulator quirk, not a cornus bug; configured via the `AZURE_STORAGE_ACCOUNT`/`AZURE_STORAGE_KEY`/`AZURE_STORAGE_DOMAIN`/`AZURE_STORAGE_PROTOCOL`/`AZURE_STORAGE_IS_LOCAL_EMULATOR` envs). Exact repro commands are in TESTING.md.
- `make e2e-cloudblob` builds a tag-gated `cornus-cloudblob` binary and runs `registry-gcs.star` + `registry-azblob.star` against the emulators. Gotcha: the SERVED cornus binary needs `-tags cloudblob`, not the e2e runner.
- winterbaume S3 mock: use **v0.2.5 or later**. v0.2.4 UTF-8-validated binary `UploadPart` bodies and 400'd every gzip layer (`MalformedXML: invalid utf-8 ... index 1` — 0x8b, the second gzip magic byte); real S3 never does this. Prebuilt binaries are on GitHub releases (`moriyoshi/winterbaume`).
- Manifest PUT is **permissive by tag but verified by digest** (post-2026-07-09-audit). Tag pushes store the raw body with no schema / ref-existence check; a by-digest PUT whose body hashes differently from the `sha256:` reference is now rejected 400 DIGEST_INVALID. See [[codebase-audit-2026-07]].
- The deploy pull-ref registry host is **decoupled from the client control-plane endpoint** — it comes from the server's `GET /.cornus/v1/info` (`RegistryHost` / `RegistryScheme`), so cluster nodes pull from the right (often co-located) registry instead of the client's loopback port-forward. See [[remote-cluster-connection-ergonomics]].

## Details

### Storage architecture (internal/storage)

- `filesystem.go`: native, default, zero-dependency backend.
- `blob.go`: gocloud `*blob.Bucket` — `mem://`, `s3://`, and (tag-gated) `gs://` / `azblob://`.
- `open.go`: routes a ref (bare path / `file://` / `mem://` / `s3://bucket?...`) to a backend. `openS3Bucket` returns the `*s3.Client` so one client backs both `s3blob.OpenBucket` and the native store. Only `s3://` uses `s3ObjectStore`; mem/gs/azblob keep the staged `blobObjectStore`.
- gocloud `OpenBucket` is lazy — the bucket must exist before the first op (tests create it via `s3.CreateBucket`, tolerating already-exists).
- gocloud `List` returns "directory" entries with `IsDir` and a `Key` ending in `/`, matching the filesystem backend's convention; the `Backend` layer treats both the same.
- Static `CGO_ENABLED=0` build still works; gocloud + aws-sdk-go-v2 grew the binary 27M → 35M. `go-containerregistry` needed `go-connections@0.7.0`; grpc bumped 1.67 → 1.79 with no buildkit regression.

### S3 native multipart upload (NativeUploader)

The load-bearing constraint is cross-request resumability: each OCI PATCH is a separate HTTP request, so all upload state must persist server-side.

- `NewUpload` -> `CreateMultipartUpload` to temp key `uploads/<id>`.
- `Write` reads in 1 MiB blocks into both a running sha256 and a pending tail; at >=5 MiB the tail flushes as the next `UploadPart`. Memory stays ~6 MiB for any chunk size (one huge PATCH makes many parts; many tiny PATCHes coalesce).
- State persists in a JSON sidecar `s3upload-<id>.json` in the staging dir: uploadId, part ETags/sizes, total, base64 <5 MiB tail, and the sha256 state via `encoding.BinaryMarshaler`. Staging drops from O(blob) to O(<5 MiB). `GetUpload` reloads the sidecar and reconstructs the live hash so a later request resumes exactly.
- `Commit` computes the digest; on `expect` mismatch it `AbortMultipartUpload`s, deletes the sidecar, and returns `ErrDigestMismatch`. Otherwise it flushes the final tail part, `CompleteMultipartUpload` at the temp key, server-side `CopyObject` temp -> `blobKey(digest)`, and deletes temp + sidecar (dedup-skips if the CAS key already exists). Empty blob (no parts) aborts the multipart and writes via normal `Put`.
- Known limit: single-request `CopyObject` caps at a 5 GiB source, so >5 GiB layers would need `UploadPartCopy` (multipart copy). Not needed for typical registry blobs.
- `config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired)` was tested as a workaround for the winterbaume v0.2.4 bug — it did NOT change the result and was reverted unvalidated; noted as a candidate S3-compat default for the future.

### Cloud drivers behind the cloudblob build tag

- `internal/storage/drivers_cloud.go` (`//go:build cloudblob`) blank-imports `gocloud.dev/blob/gcsblob` + `azureblob` and sets `const cloudBlobBuilt = true`; `drivers_nocloud.go` (`//go:build !cloudblob`) sets it false.
- Rationale: keeps the Google/Azure SDKs out of the default single-static-binary (105 MB baseline unchanged).
- CI runs `go build -tags cloudblob ./internal/storage/...` so the tagged path compiles without pulling drivers into the default binary.

Emulator validation: both gated round-trip tests (`CORNUS_TEST_GCS` / `CORNUS_TEST_AZBLOB`) pass against local emulators with zero code changes.

- **GCS via fake-gcs-server**: gocloud's gcsblob driver honors `STORAGE_EMULATOR_HOST` natively; point it at the emulator and the driver just works.
- **Azure via Azurite**: Azurite must run with `--skipApiVersionCheck` because the Azure SDK sends the 2026-06-06 API version, which Azurite otherwise rejects — an emulator quirk, not a cornus bug. The driver is steered at the emulator via `AZURE_STORAGE_ACCOUNT`, `AZURE_STORAGE_KEY`, `AZURE_STORAGE_DOMAIN`, `AZURE_STORAGE_PROTOCOL`, and `AZURE_STORAGE_IS_LOCAL_EMULATOR`.
- The exact emulator start + test repro commands are documented in `.agents/docs/TESTING.md`.
- Still open: neither backend has ever been run against real (non-emulator) GCS/Azure — that needs actual cloud credentials.

### Registry wire-protocol edges

A generic `http(method, url, body?, headers?)` E2E harness builtin (returns `status`/`body`/`headers` keyed by canonical Go header names; registered in `predeclared()` + `predeclaredNames()`) drives `e2e/scenarios/registry-edges.star` against the real `internal/registry/registry.go`:

- HEAD manifest-by-tag (200 + digest/length)
- resumable/chunked upload: POST session -> PATCH -> PUT `?digest=` -> HEAD blob
- cross-repo blob mount (`?mount=&from=` -> 201)
- `DIGEST_INVALID` rejection (mismatched monolithic POST -> 400)
- manifest DELETE; unsupported blob DELETE (405); missing-blob 404 `BLOB_UNKNOWN`

Two registry facts (registry code is the source of truth): manifest delete-by-tag is unsupported because `storage.DeleteManifest` `ParseDigest`s the ref, so deletion must use the digest from the HEAD `Docker-Content-Digest` header; and Go's HTTP server strips HEAD response bodies, so `BLOB_UNKNOWN` body assertions need a follow-up GET.

### Manifest PUT validation

Tag pushes stay permissive: `storage.PutManifest` (`pkg/storage/cas.go`) writes the raw request body as a blob + per-repo membership marker + tag with NO JSON parse, NO schema check, and NO referenced-blob-existence check. An explicit request `Content-Type` is stored verbatim; only an empty one triggers `detectMediaType`, which silently falls back to the OCI manifest media type on any parse failure. So a malformed manifest, or one referencing a missing blob, still succeeds with 201 when pushed by tag.

By-digest PUT is verified (added by the 2026-07-09 audit, `handleManifest` in `pkg/registry/registry.go`). `PutManifest` stores under and returns the body's computed digest; the handler then checks `storage.ParseDigest(ref)` and, if the reference is a `sha256:` digest that does not equal the computed digest, rejects with 400 DIGEST_INVALID (`provided digest <X> does not match computed digest <Y>`), per OCI. Note the blob is persisted under its computed digest before the check, so the rejection concerns the client's claimed reference, not a refusal to store. `e2e/scenarios/registry-errors.star` (section 8) locks this: a by-digest manifest PUT with a mismatched body must return 400 DIGEST_INVALID. The same scenario also regression-locks the still-permissive tag-push path, so a future move to full schema / ref-existence validation stays a deliberate, visible change. See [[codebase-audit-2026-07]].

### Registry host advertisement (deploy pull-ref decoupling)

Core invariant: an image's identity is its **repository path** (`<project>-<service>`); the **host** is a per-vantage rendezvous detail. Push and pull hit the same co-located registry backend addressed differently, so the ref's repo path stays fixed while the host varies (push -> loopback, pull -> advertised).

The deploy pull-ref registry host is decoupled from the client control-plane endpoint. Previously `cornus compose up` derived the ref as `client.Host() + "/" + resource + ":latest"` (the scheme-stripped client endpoint), which only works on the single-node quick start where a port-forward collapses every vantage onto one loopback. On a real cluster the node pulls in the host netns with node DNS (not CoreDNS, not the client's machine), so a port-forward's `127.0.0.1:<ephemeral>` (baked in by `clientconn.go`) is unpullable.

Resolution: `runtime.registryHostFor(ctx)` resolves the ref host with precedence **override > server `/.cornus/v1/info` > `client.Host()`** (memoized), feeding `commands.go`. The override is `clientconfig.Context.RegistryHost` via `--registry` / `CORNUS_REGISTRY`, carried on `clientconn.Conn.RegistryHost` and never rewritten by the port-forward. The auth-exempt `GET /.cornus/v1/info` returns `api.ServerInfo{RegistryHost, RegistryScheme}`, sourced from `CORNUS_ADVERTISE_REGISTRY` (mirrors `CORNUS_ADVERTISE_URL`) else the optional `deploy.RegistryAdvertiser` the kubernetes backend implements by introspecting its **own** Service (reusing the `clusterDNSIP` Service-read pattern + svcforward's `app.kubernetes.io/name=cornus` / `app=cornus` selectors). Only **NodePort** (`localhost:<nodePort>`) and **LoadBalancer** auto-advertise; ClusterIP returns empty on purpose (default type, no intent signal, and advertising it would break the quick start whose node only trusts `localhost:<nodePort>`). `Server.advertisedRegistry` short-circuits to empty for non-kubernetes `CORNUS_DEPLOY_BACKEND` before calling `getBackend()`, so the build hot path never constructs a dockerhost / containerd backend just to learn it does not advertise.

Push-redirect: `Server.localPushTarget` rewrites a build push whose target host equals the advertised host to the co-located registry over loopback (`127.0.0.1:<port>` from `HTTPAddr`), because the in-pod build engine cannot reach a NodePort's `localhost:<nodePort>`; the repo path is preserved so push and pull hit the same content. Applied in `build.go` and `build_attach.go`.

Push vs pull vantage asymmetry: the build engine pushes from **inside the pod**; the node pulls from the **host netns**. A single tag host rarely satisfies both. Node containerd uses host DNS, not CoreDNS, so a `*.svc` name does not resolve at pull time even though the ClusterIP it would return is reachable via kube-proxy from the host netns — hence ClusterIP-advertise carries the IP (not the DNS name) and is opt-in via `CORNUS_ADVERTISE_REGISTRY`, while NodePort / LB auto-advertise. NetworkPolicy is orthogonal to ClusterIP-vs-NodePort: both DNAT to the same registry pod, so a default-deny ingress drops node-origin pull traffic either way; the source is a node IP (host netns), matchable only by an `ipBlock`, never a `podSelector`. Only a host-level listener (hostPort / hostNetwork) is policy-immune.

Deployment surface: Helm `registry.exposure` (`nodePort|clusterIP|hostPort|hostNetwork|ingress`, default **nodePort**) drives the Service type / hostPort / hostNetwork / `CORNUS_ADVERTISE_REGISTRY` env, plus a `registry.nodeCIDR` NetworkPolicy ipBlock allow for the pod-terminating modes. The raw manifest `deploy/k8s/cornus.yaml` Service is **NodePort `30500`** (matching the chart default), so the quick start uses **no port-forward at all** — the node pulls through kube-proxy's node-port binding (reachable on the node's own loopback via `route_localnet`) and the same node port also serves the CLI control plane. NodePort node-portability relies on `route_localnet`; hostPort binds one node (pin with nodeSelector). See [[remote-cluster-connection-ergonomics]].

### Registry-over-S3 E2E

`e2e/scenarios/registry-s3.star` serves the registry over `s3://cornus?endpoint=...` and round-trips a real OCI image (push/pull + catalog + tags, all from object storage). Kept OUT of the default Makefile `SCENARIOS` list — it needs an S3 server on 127.0.0.1:5557 with a `cornus` bucket. On-host runner: `.agents-workspace/tmp/registry-s3-e2e-run.sh` (starts winterbaume-server, creates the bucket, runs with `--target local`; unprivileged). Passes on winterbaume v0.2.5, proving binary-blob `UploadPart` -> `CompleteMultipartUpload` -> `CopyObject` end to end.

### Registry-over-GCS/Azure E2E

`e2e/scenarios/registry-gcs.star` and `e2e/scenarios/registry-azblob.star` follow the registry-s3.star pattern (`serve(storage=...)` + full registry HTTP surface round-trip) with env-gated self-skip, so they are harmless in default runs. Both have PASSED LIVE against fake-gcs-server and Azurite respectively, driving the full registry HTTP surface from cloud object storage. `make e2e-cloudblob` is the one-command runner: it builds a `cornus-cloudblob` binary with `-tags cloudblob` and runs both scenarios. The build-tag gotcha to remember: the tag must be on the SERVED cornus binary (the one `serve(storage=...)` launches), not on the `cornus-e2e` runner.

## Files

- `internal/storage/cas.go` — shared `Backend` layer (CAS, manifests, tags, uploads)
- `internal/storage/filesystem.go` — native filesystem backend (default)
- `internal/storage/blob.go` — gocloud blob backend
- `internal/storage/s3.go` — `s3ObjectStore` with `NativeUploader` multipart
- `internal/storage/open.go` — ref routing; `openS3Bucket`
- `internal/storage/drivers_cloud.go` / `drivers_nocloud.go` — cloudblob tag gating
- `internal/registry/registry.go` — registry HTTP handlers
- `pkg/registry/registry.go` — `handleManifest` by-digest DIGEST_INVALID verification
- `pkg/storage/cas.go` — `PutManifest` (permissive tag-push storage)
- `pkg/server` — `/.cornus/v1/info` (`ServerInfo`), `Server.localPushTarget`, `Server.advertisedRegistry`, `parseAdvertiseRegistry`; applied in `build.go` / `build_attach.go`
- `pkg/deploy/kubernetes` — `RegistryAdvertiser` (own-Service introspection)
- `cmd/cornus/internal/composecli` — `runtime.registryHostFor`, `commands.go` ref derivation
- `deploy/k8s/cornus.yaml` — quick-start Service (NodePort `30500`)
- `e2e/scenarios/registry-errors.star` — NAME_UNKNOWN / UNSUPPORTED / DIGEST_INVALID + manifest-validation regression lock
- `e2e/scenarios/registry-advertise.star` — `/.cornus/v1/info` echo + push-redirect scenario (in `SCENARIOS`)
- `e2e/scenarios/registry-edges.star` — wire-protocol edges scenario
- `e2e/scenarios/registry-s3.star` — registry-over-S3 scenario (opt-in)
- `e2e/scenarios/registry-gcs.star` / `e2e/scenarios/registry-azblob.star` — registry-over-GCS/Azure scenarios (env-gated self-skip; run via `make e2e-cloudblob`)
- `.agents-workspace/tmp/registry-s3-e2e-run.sh` — on-host registry-s3 runner

## Test Coverage

- `internal/storage/storage_test.go` — full CAS / chunked-upload / manifest / tag / repo / dedup suite, table-driven across filesystem + memory.
- `internal/registry/registry_test.go` — `TestBackendsConformance` runs go-containerregistry push/pull/tags over filesystem + memory.
- `internal/storage/s3_test.go` — `TestS3Backend` + `TestS3MultipartUpload`, opt-in via `CORNUS_TEST_S3_ENDPOINT` (e.g. `http://127.0.0.1:5555`), skip otherwise. Multipart test uploads >5 MiB in 3 chunks, reconstructs the session via `GetUpload` between writes (proves sidecar resumption), and asserts wrong-digest commit yields `ErrDigestMismatch` with no blob.
- `internal/storage/cloudblob_test.go` (`//go:build cloudblob`) — opt-in GCS/Azure round-trips via `CORNUS_TEST_GCS` / `CORNUS_TEST_AZBLOB`; both PASS against fake-gcs-server and Azurite with zero code changes; `drivers_nocloud_test.go` asserts the clear default-build error.
- `e2e/scenarios/registry-edges.star` — in default Makefile `SCENARIOS`; runs unprivileged with `--target local`.
- Manifest-validation + registry-host advertisement: `pkg/deploy/kubernetes/advertise_test.go` (table over Service types via `NewWithClient` + fake clientset), `pkg/server/server_info_test.go` (`parseAdvertiseRegistry`, env advertise, `localPushTarget` redirect / external / digest, `TestAdvertisedRegistryNonK8sBackendSkipsIntrospection`), `cmd/cornus/internal/composecli/registry_test.go` (precedence + memoization via an httptest `/.cornus/v1/info`). E2E: `registry-errors.star` section 8 (by-digest mismatch -> 400 DIGEST_INVALID) and `registry-advertise.star` (server redirects an unreachable-tag-host `build_upload` push into its co-located registry).
- Not E2E-covered: the auto-advertise-from-Service path (NodePort / LB) and a full in-cluster deploy pulling with no port-forward — the harness `kube` target runs cornus **host-side**, so it has no self Service to introspect (the Service-introspection logic is unit-tested in `advertise_test.go`).
- `e2e/scenarios/registry-s3.star` — passes on winterbaume v0.2.5.
- `e2e/scenarios/registry-gcs.star` / `registry-azblob.star` — passed live against the emulators through the full registry HTTP surface (`make e2e-cloudblob`).
- Still open: a real-cloud (non-emulator) GCS/Azure run has never happened — needs actual cloud credentials.

## Pitfalls

- **winterbaume v0.2.4 400s binary UploadPart bodies** (`MalformedXML: invalid utf-8 sequence`); ASCII part data passes coincidentally, so synthetic tests can miss it — always test with binary (gzip) payloads. Use **v0.2.5+**.
- gocloud `OpenBucket` is lazy: create the bucket before the first operation or ops fail.
- `gs://` / `azblob://` do nothing without `-tags cloudblob`; do not assume gocloud schemes "just work" via the pass-through. And the tag belongs on the SERVED cornus binary, not the e2e runner — `make e2e-cloudblob` gets this right.
- Azurite rejects the Azure SDK's 2026-06-06 API version unless started with `--skipApiVersionCheck` — an emulator limitation, not a cornus bug.
- Manifest DELETE requires a digest ref, not a tag (`storage.DeleteManifest` parses the ref as a digest).
- HEAD responses have their bodies stripped by Go's HTTP server — use GET when asserting error bodies.
- aws-sdk logs benign "Response has no supported checksum" warnings against winterbaume (no response checksums); harmless. The registry logs a benign "unknown type: cacheconfig.v0" for cache-config blobs; they store and round-trip fine.
- Single-request `CopyObject` limits blobs to 5 GiB on the S3 multipart commit path.
- Manifest PUT is validated only for the **by-digest** case; a malformed manifest or one referencing a missing blob still succeeds with 201 when pushed **by tag** (`PutManifest` does no schema / ref-existence check). Do not assume the registry rejects bad manifests on tag push.
- The deploy ref host does NOT track the client control-plane endpoint on a real cluster — it comes from `/.cornus/v1/info` (or `--registry` / `CORNUS_REGISTRY`). ClusterIP is never auto-advertised (opt in with `CORNUS_ADVERTISE_REGISTRY`); the harness `kube` target runs cornus host-side, so it never exercises the own-Service auto-advertise path.
