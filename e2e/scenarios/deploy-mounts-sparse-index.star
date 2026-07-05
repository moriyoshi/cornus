# SPARSE client-local mount indices — the "m2 gap" regression guard.
#
# The client names each client-local bind mount `m<i>` by its index in the FULL
# `spec.Mounts` list and SKIPS non-local entries (pkg/client/client.go
# resolveLocalMounts). So a NON-LOCAL source (a named / bare-name volume) sitting
# BETWEEN two client-local binds makes the served names sparse — `m0`, `m2`, with
# no `m1` — while `LocalMount.Index` still points at the original slot. Every
# consumer must key off that Index (and Name), never off its own dense loop
# position:
#
#   pkg/deploywire/backing.go  MountManager.Prepare -> mounts[lm.Index].Source = <9P mountpoint>
#   pkg/server/deploy_attach.go                     -> Target: spec.Mounts[lm.Index].Target
#
# A dense re-index rewrites the WRONG slot: the named volume at index 1 gets m2's
# 9P mountpoint and the real /mnt/c entry is left pointing at the caller's raw
# path. Historically the caretaker side asked the client for a name it never
# served and the mount died with "connection reset by peer".
#
# This can ONLY be driven from a RAW DeploySpec: both Compose
# (pkg/compose/project.go) and the Docker API proxy (pkg/dockerproxy/translate.go)
# route named volumes into `spec.Volumes`, never into `spec.Mounts`, so no
# compose-tier scenario — compose-mounts-multi.star included — can produce the
# gap. `deploy_attach(mounts=[...])` writes the mount list into the deploy spec
# FILE verbatim, exactly as a hand-written `cornus deploy -f spec.json` would,
# which is what makes the interleaving expressible at all (`--local-mount` flags
# are appended after the file's mounts and cannot sit between them).
#
# docker target only: mounts-only deploy-attach on dockerhost takes the
# single-host kernel-9P path (pkg/server/deploy_attach.go applyWithHostMounts ->
# MountManager.Prepare), which needs root + the 9p module — the containerized
# runner. kubernetes cannot host this shape at all: ApplyWithMounts rejects any
# spec.Mounts entry with no client-local 9P backing (never a hostPath), so the
# middle named volume would be refused before the index mapping is reached.
# containerd/incus reject client-local mounts outright on this path.

NAME = "sparsemnt"
VOL = "sparsemidvol"  # bare-name (NON-local) source: a plain Docker named volume

if TARGET != "docker":
    log("deploy-mounts-sparse-index: skipped (docker-only; the dockerhost kernel-9P host-mount path)")
else:
    # Up-front cleanup (Starlark has no defer, so end-of-scenario cleanup does not
    # run on failure): a leftover container from a failed run would hold the named
    # volume, and a leftover volume would carry stale content into the
    # "the middle volume stayed empty" assertion below.
    sh(cmd = "docker rm -f $(docker ps -aq -f label=cornus.app=%s) 2>/dev/null; docker volume rm -f %s 2>/dev/null; true" % (NAME, VOL))

    # CORNUS_ALLOW_BIND_SOURCES must cover the bare volume name: the host
    # bind-source policy (pkg/deploy/hostpolicy) validates EVERY spec.Mounts
    # source, and a bare name is not under the "/" prefix the docker target sets.
    # "/" still covers the server-side 9P mountpoints the MountManager mints.
    addr = serve(env = {"CORNUS_ALLOW_BIND_SOURCES": "/," + VOL})

    # Two client-local dirs, each with its OWN marker, so a mis-routed stream is
    # visible as content from the wrong dir rather than as a missing file.
    da = temp_dir()
    dc = temp_dir()
    write_file(path = da + "/marker", content = "SPARSE-A")
    write_file(path = dc + "/marker", content = "SPARSE-C")
    log("client-local dirs: %s (m0) and %s (m2, sparse — index 1 is a named volume)" % (da, dc))

    # spec.Mounts = [client-local, NAMED VOLUME, client-local]  ->  m0, m2.
    # Read-write (no :ro) so the write-back assertion below can prove the m2
    # stream really terminates in dc.
    deploy_attach(
        name = NAME,
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        mounts = [
            da + ":/mnt/a",
            VOL + ":/mnt/mid",
            dc + ":/mnt/c",
        ],
        timeout = "240s",
    )
    log("✓ deploy-attach reached ready with a sparse (m0, m2) local-mount set")

    def run(cmd):
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, NAME, "sh", "-c", cmd])
        return got["output"]

    # Each client-local mount must carry its OWN dir's content at its OWN target.
    # If a consumer re-indexed densely, /mnt/c would be bound to the caller's raw
    # path (dockerhost and the client share a host here, so that alone can still
    # read SPARSE-C) — which is exactly why the /mnt/mid assertion below, not this
    # one, is the load-bearing check.
    assert_contains(run("cat /mnt/a/marker"), "SPARSE-A", "m0 must serve its own dir at /mnt/a")
    assert_contains(run("cat /mnt/c/marker"), "SPARSE-C", "m2 (sparse index) must serve its own dir at /mnt/c")
    log("✓ both client-local mounts serve their own content at their own targets")

    # The non-local middle entry must remain a plain empty named volume. A dense
    # re-index would have rewritten THIS slot to a 9P mountpoint, so a marker
    # showing up here is the regression.
    mid = run("ls -A /mnt/mid; echo END")
    assert_true("SPARSE" not in mid and "marker" not in mid, "the interleaved named volume was rewritten to a client-local 9P mount (got %r)" % mid)
    assert_contains(mid, "END", "could not list the interleaved named volume's target")
    log("✓ the interleaved named volume stayed an empty non-9P mount")

    # ...and it is a REAL Docker named volume, not a bind of some server path.
    vols = docker("volume", "ls", "--format", "{{.Name}}")
    assert_true(VOL in [line.strip() for line in vols.split("\n")], "the bare-name mount source did not become a Docker named volume")
    log("✓ the interleaved bare-name source was realized as a Docker named volume")

    # Write-back through the SPARSE mount: what the container writes at /mnt/c
    # must land in dc — the direction check that a swapped index cannot fake.
    run("printf %s FROM-CONTAINER-C > /mnt/c/frompod")
    back = read_file(path = dc + "/frompod", default = "MISSING")
    assert_contains(back, "FROM-CONTAINER-C", "m2's writable 9P stream is not wired to its own client dir")
    assert_eq(read_file(path = da + "/frompod", default = "ABSENT"), "ABSENT", "m2's write leaked into m0's client dir (crossed streams)")
    log("✓ the sparse-index mount writes back to its own client dir only")

    # A write into the middle named volume must stay in the volume: it has no
    # client backing at all.
    run("printf %s IN-VOLUME > /mnt/mid/involume")
    assert_eq(read_file(path = da + "/involume", default = "ABSENT"), "ABSENT", "the named volume's write leaked into a client dir")
    assert_eq(read_file(path = dc + "/involume", default = "ABSENT"), "ABSENT", "the named volume's write leaked into a client dir")
    log("✓ the named volume's writes stayed in the volume")

    # Graceful disconnect tears the deployment down and unwinds both 9P mounts.
    attach_stop(name = NAME)
    sh(cmd = "docker volume rm -f %s 2>/dev/null; true" % VOL)
    log("torn down")
