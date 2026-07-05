# The web BFF's file explorer against a REAL deploy backend: the `/.cornus/web/fs*`
# surface driven end to end over a live workload.
#
# Why this exists as an E2E rather than another Go test. The webbff unit suite drives the
# explorer against a fake containerFS, which can prove where the BFF SENT a request (a
# fake whose every method calls t.Fatal is how "no byte reached the daemon" is asserted)
# but never that the answer is right. Only a real backend can show that a file streamed
# out of a container comes back byte for byte, that a refusal the BFF issues is the one
# the kernel would have issued, and that the directory a container path was redirected
# onto is the directory the container is actually reading.
#
# THE TWO ARMS ARE COMPLEMENTARY, and that is the architectural fact this scenario pins:
#
#   - On docker/containerd/bare the archive primitives work, so a copy that CROSSES the
#     client/server boundary moves bytes through the RELAY. Since 2026-08-08 those three
#     also implement deploy.FSOperator over /proc/<pid>/root, so a copy WITHIN one
#     workload runs server-side instead — asserted below, and privilege-dependent, since
#     reading a root-owned container's rootfs needs root. Client-local bind mounts need
#     root on those targets to kernel-9p-mount (see deploy-mounts.star), so the redirect
#     is out of reach here. incus implements deploy.FSOperator too since 2026-08-08, by a
#     different route: it has no local path to serve, so it uses the daemon's own SFTP
#     channel into the instance (pkg/fsop.SFTPFS). The two routes are held to ONE contract
#     (pkg/fsop.RunFSContract), which is the point of the FS seam existing at all.
#   - On kubernetes there are TWO routes and they cover different ground. A volume-backed
#     path is served structurally by the caretaker (fsop), which needs nothing from the
#     app's image and keeps a volume-to-volume copy inside the pod. An IMAGE path is
#     served by tar over exec (pkg/deploy/kubernetes/archive.go) — the kubectl cp
#     mechanism — which is the only thing that can reach a mount namespace the caretaker
#     does not share. The bind redirect, realized by the mount sidecar, remains the
#     reason the explorer is fast there.
#
#     Both are asserted below, and so is the DIVISION between them: a volume path must
#     take the operator (a route regression there is invisible at runtime — the copy
#     still works, just the slow way) while an image path must still deliver its bytes.
#
# Each arm therefore asserts its own positive AND the other arm's negative, so neither
# can quietly stop being true.
#
# Opt-in: `make e2e-web-fs` (docker by default, `TARGET=kube` for the other arm); not in
# the default SCENARIOS list. The local target has no runtime backend, so it skips.

project = "webfs"
app = "webfs-app"

# The kube arm needs cornus:e2e (its image doubles as the mount-agent sidecar, and the
# target has already loaded it into the cluster); every other target just needs a small
# public image with a shell, so it does not depend on a build.
def compose_yaml(image, volumes):
    return """name: %s
services:
  app:
    image: %s
    entrypoint: ["sleep"]
    command: ["3600"]
%s""" % (project, image, volumes)

if TARGET == "local":
    log("web-fs: skipped (needs a real backend)")

elif TARGET == "kube":
    # ---- the client-local bind redirect ----------------------------------------
    #
    # The whole project lives in a temp dir: the explorer's `project` root IS the compose
    # file's directory, so anything a test copies there must not land in the committed
    # tree, and the bind source is disposable with it.
    proj = temp_dir()
    ro = temp_dir()
    compose_file = proj + "/compose.yaml"

    write_file(path = proj + "/data/from-client.txt", content = "WRITTEN-BY-THE-HARNESS\n")
    write_file(path = ro + "/keep.txt", content = "ORIGINAL\n")

    # Two named volumes alongside the binds. They are what the fsop section below needs,
    # and they must ride the SAME service: the caretaker that serves them is the one the
    # bind mounts already put in this pod, and a structured filesystem operation is looked
    # up by that pod's instance identity.
    write_file(path = compose_file, content = compose_yaml("cornus:e2e", """    volumes:
      - ./data:/data
      - %s:/ro:ro
      - vol1:/vol1
      - vol2:/vol2
volumes:
  vol1:
  vol2:
""" % ro))

    kube_addr = serve()
    kube_srv = "http://" + kube_addr
    compose_up(file = compose_file, project = project, detach = True)
    wait(name = app, running = 1, timeout = "300s")
    base = web(compose_file = compose_file, project = project)

    # The redirect names the RIGHT directory. A fatal-fake unit test proves the daemon
    # was not called; only the container can say the bytes went where it is reading.
    lst = http_get(url = base + "/.cornus/web/fs?source=virtual&path=" + app + "/data")
    assert_eq(lst["status"], 200, "listing a bind-backed container path: %s" % lst["body"])
    assert_contains(lst["body"], "from-client.txt", "the bind source's own file should be listed")

    w = http(
        method = "PUT",
        url = base + "/.cornus/web/fs/content?source=virtual&path=" + app + "/data/via-bff.txt",
        body = "THROUGH-THE-BFF\n",
    )
    assert_eq(w["status"], 200, "writing a bind-backed container path: %s" % w["body"])

    # Both sides of the same claim. Either alone would be satisfied by a BFF that wrote
    # to the wrong one of them.
    assert_contains(read_file(path = proj + "/data/via-bff.txt"), "THROUGH-THE-BFF",
                    "the write did not land on the developer's disk")
    assert_contains(pod_exec(app = app, cmd = "cat /data/via-bff.txt"), "THROUGH-THE-BFF",
                    "the container does not see what the BFF wrote to its bind source")

    # And the converse, so the above is not merely "a file exists somewhere": what the
    # CONTAINER writes is what the explorer reads through the container spelling.
    pod_exec(app = app, cmd = "echo FROM-THE-CONTAINER > /data/from-container.txt")
    back = http_get(url = base + "/.cornus/web/fs/content?source=virtual&path=" + app + "/data/from-container.txt")
    assert_eq(back["status"], 200, "reading a file the container wrote")
    assert_contains(back["body"], "FROM-THE-CONTAINER", "the explorer read a stale or wrong file")
    log("✓ the bind-backed container path is the developer's own directory, and both views agree")

    # Preflight says where the work will run, and `here` depends on api.Origin matching
    # this host and directory — a gate that only exists in production shape.
    pre = http(
        method = "POST",
        url = base + "/.cornus/web/fs/preflight?op=copy&source=virtual&path=project",
        body = '{"to":"%s/data","items":[{"from":"project/data/from-client.txt","to":"%s/data/pre.txt"}]}' % (app, app),
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(pre["status"], 200, "preflight: %s" % pre["body"])
    item = json.decode(pre["body"])["items"][0]
    assert_eq(item["route"], "here",
              "a bind-backed destination must run on the client, got route=%r why=%r" % (item["route"], item.get("why", "")))

    # THE IMAGE-LAYER PATH: outside every volume, so the caretaker cannot see it, and
    # reachable only by tar over exec.
    #
    # Read the split precisely, because the first version of this assertion got it wrong
    # and the run corrected it: an image path LISTS via exec (the listing is a shell glob)
    # and, since archive.go, also COPIES. Asserting that listing fails was simply false,
    # and the failure is what exposed the workdir defect below.
    img = http_get(url = base + "/.cornus/web/fs?source=virtual&path=" + app + "/etc")
    assert_eq(img["status"], 200, "an image path still LISTS on kubernetes (exec works): %s" % img["body"])
    assert_contains(img["body"], "hostname", "listing /etc must return /etc — not the image's default workdir")

    # The copy this backend could not do until archive.go existed. Asserting the STATUS
    # alone would be satisfied by an empty file, so the bytes are compared against what
    # the container itself reads — the only witness that the tar carried real content out
    # of the image layer rather than out of thin air.
    want_hostname = pod_exec(app = app, cmd = "cat /etc/hostname").strip()
    assert_true(want_hostname != "", "the pod reported an empty /etc/hostname; nothing to compare against")
    cp = http(
        method = "POST",
        url = base + "/.cornus/web/fs/copy?source=virtual&path=" + app + "/etc/hostname",
        body = '{"to":"project/hostname"}',
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(cp["status"], 200,
              "copying an image-layer path must now work through tar-over-exec: %d %s" % (cp["status"], cp["body"]))
    got = http_get(url = base + "/.cornus/web/fs/content?source=local&root=project&path=hostname")
    assert_eq(got["status"], 200, "reading back the copied image file: %s" % got["body"])
    assert_contains(got["body"], want_hostname,
                    "the copied file does not hold what the container reads at /etc/hostname")

    # ...and it went the only way it could have. An image path has no operator root, so a
    # preflight claiming `server` would mean the planner is about to ask the caretaker for
    # something it cannot serve — the failure this scenario existed to catch, inverted.
    ipre = http(
        method = "POST",
        url = base + "/.cornus/web/fs/preflight?op=copy&source=virtual&path=" + app + "/etc",
        body = '{"to":"project","items":[{"from":"%s/etc/hostname","to":"project/pre.txt"}]}' % app,
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(ipre["status"], 200, "image-path preflight: %s" % ipre["body"])
    iitem = json.decode(ipre["body"])["items"][0]
    assert_true(iitem["route"] != "server",
                "an image-layer path has no caretaker root, so it must NOT be planned as a server-side " +
                "operation; got route=%r why=%r" % (iitem["route"], iitem.get("why", "")))
    log("✓ an image-layer path listed, copied through tar-over-exec, and did not claim the operator route")

    # The read-only bind: browsable, unwritable, and the BFF and the KERNEL agree. That
    # agreement is the contract — each side alone is individually defensible and only
    # their consistency is the promise.
    roots = json.decode(http_get(url = base + "/.cornus/web/fs/roots")["body"])
    ro_id = ""
    for r in roots["roots"]:
        if r.get("readOnly", False):
            ro_id = r["id"]
    assert_true(ro_id != "", "the `:ro` bind source should be a browsable READ-ONLY root, got %r" % roots["roots"])

    ro_list = http_get(url = base + "/.cornus/web/fs?source=local&root=" + ro_id + "&path=")
    assert_eq(ro_list["status"], 200, "a read-only root must still be browsable")
    assert_contains(ro_list["body"], "keep.txt")
    ro_write = http(
        method = "PUT",
        url = base + "/.cornus/web/fs/content?source=local&root=" + ro_id + "&path=keep.txt",
        body = "CLOBBERED\n",
    )
    assert_eq(ro_write["status"], 403, "writing a read-only bind must be 403, got %d" % ro_write["status"])
    assert_contains(read_file(path = ro + "/keep.txt"), "ORIGINAL", "the read-only root was mutated")
    kernel = pod_exec(app = app, cmd = "echo x > /ro/keep.txt 2>/dev/null && echo CONTAINER-WROTE || echo CONTAINER-REFUSED")
    assert_contains(kernel, "CONTAINER-REFUSED",
                    "the kernel allows what the BFF refuses: the two disagree about this mount")
    log("✓ the read-only bind is browsable, unwritable, and the BFF and the kernel agree")

    # ---- volume-backed paths, served by the caretaker's filesystem operator ------
    #
    # This is the section the archive arm cannot have, and it is why the operator exists
    # even now that kubernetes HAS an archive. Tar over exec needs a tar in the app's
    # image and pays a subprocess and a re-pack for every transfer; the caretaker needs
    # neither, reports real errnos, and keeps a volume-to-volume copy inside the pod.
    # Before either existed, a volume-backed path could be LISTED (exec works) and then
    # neither read, written, nor copied — a directory whose every file answered 501.
    #
    # Note what each assertion is really pinning: the first two are the archive-to-fsop
    # fallback (a primitive this backend does not have, answered by one that it does),
    # and the third is the native route, where the bytes never leave the pod at all.
    pod_exec(app = app, cmd = "echo IN-THE-VOLUME > /vol1/seed.txt")

    # Ask the SERVER's own surface first, before the BFF's. The BFF collapses every way
    # this can fail into one status, so when it breaks the message says nothing about
    # which link went: no operator on the backend, no caretaker registered for the pod, or
    # no root covering the path. This assertion names the link.
    # No retry needed and none available on `http`: by this point the bind assertions
    # above have already proved this pod's caretaker is connected and serving, and the
    # operator registers its tag on that same connection before any role starts.
    op = http(
        method = "POST",
        url = kube_srv + "/.cornus/v1/deploy/" + app + "/fsop?op=stat&path=/vol1",
    )
    assert_eq(op["status"], 200,
              "the server's own fsop endpoint must serve a volume path: %d %s" % (op["status"], op["body"]))

    vol_read = http_get(url = base + "/.cornus/web/fs/content?source=virtual&path=" + app + "/vol1/seed.txt")
    assert_eq(vol_read["status"], 200,
              "reading a volume-backed path on kubernetes needs the operator: %s" % vol_read["body"])
    assert_contains(vol_read["body"], "IN-THE-VOLUME", "the operator returned the wrong file")

    vol_write = http(
        method = "PUT",
        url = base + "/.cornus/web/fs/content?source=virtual&path=" + app + "/vol1/written.txt",
        body = "WRITTEN-THROUGH-THE-OPERATOR\n",
    )
    assert_eq(vol_write["status"], 200, "writing a volume-backed path: %s" % vol_write["body"])
    assert_contains(pod_exec(app = app, cmd = "cat /vol1/written.txt"), "WRITTEN-THROUGH-THE-OPERATOR",
                    "the container does not see what the BFF wrote into its volume")
    log("✓ a kubernetes volume is readable and writable through the caretaker's operator")

    # The native copy. Both ends are volumes on one workload, so the operator does the
    # whole thing in the pod. There IS now an archive to relay through, which is exactly
    # why the preflight assertion below matters more than it used to: degrading to the
    # relay would still copy the file, silently, at a cost nobody would see.
    vol_copy = http(
        method = "POST",
        url = base + "/.cornus/web/fs/copy?source=virtual&path=" + app + "/vol1/seed.txt",
        body = '{"to":"%s/vol2/copied.txt"}' % app,
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(vol_copy["status"], 200, "volume-to-volume copy: %s" % vol_copy["body"])
    assert_contains(pod_exec(app = app, cmd = "cat /vol2/copied.txt"), "IN-THE-VOLUME",
                    "the volume-to-volume copy did not deliver the bytes")
    assert_contains(pod_exec(app = app, cmd = "cat /vol1/seed.txt"), "IN-THE-VOLUME",
                    "a copy consumed its source")

    # And preflight says so out loud. `server` is the claim: a wrong route here is
    # invisible at runtime — the copy still works, just the slow way — so this assertion
    # is the only thing that can catch the planner silently degrading.
    vpre = http(
        method = "POST",
        url = base + "/.cornus/web/fs/preflight?op=copy&source=virtual&path=" + app + "/vol1",
        body = '{"to":"%s/vol2","items":[{"from":"%s/vol1/seed.txt","to":"%s/vol2/pre.txt"}]}' % (app, app, app),
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(vpre["status"], 200, "volume preflight: %s" % vpre["body"])
    vitem = json.decode(vpre["body"])["items"][0]
    assert_eq(vitem["route"], "server",
              "a volume-to-volume copy must run in the pod, got route=%r why=%r" % (vitem["route"], vitem.get("why", "")))
    assert_true(vitem["native"], "a volume-to-volume copy should be native (no bytes move)")
    log("✓ volume-to-volume copy ran inside the pod, and preflight declared it")

    # A move within one operator is a rename, and the source really goes.
    vmove = http(
        method = "POST",
        url = base + "/.cornus/web/fs/move?source=virtual&path=" + app + "/vol1/written.txt",
        body = '{"to":"%s/vol1/renamed.txt"}' % app,
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(vmove["status"], 200, "renaming within a volume: %s" % vmove["body"])
    assert_contains(pod_exec(app = app, cmd = "test -e /vol1/written.txt && echo STILL-THERE || echo GONE"), "GONE",
                    "a move within one volume left its source behind")
    assert_contains(pod_exec(app = app, cmd = "cat /vol1/renamed.txt"), "WRITTEN-THROUGH-THE-OPERATOR",
                    "the renamed file did not arrive")
    log("✓ a move within one volume was a rename, and the source is gone")

    # Declared behaviour change: a bind-backed path needs no running workload, because
    # the bytes were never in the container.
    stop(name = app)
    still = http_get(url = base + "/.cornus/web/fs?source=virtual&path=" + app + "/data", retry = "10s")
    assert_eq(still["status"], 200, "a bind-backed path must stay browsable while the workload is stopped")
    assert_contains(still["body"], "via-bff.txt")
    log("✓ the bind stayed browsable with the workload stopped")

    compose_down(file = compose_file, project = project)
    # The compose file deliberately SURVIVES: the harness reaps scenario workloads with a
    # second `compose down` after the scenario body, and deleting the file out from under
    # it turns that redundant call into a noisy error. Its temp dir goes with the run.
    remove_all(path = proj + "/data")
    log("✓ web BFF file explorer verified against the live kube backend (redirect arm)")

else:
    # ---- the relay: every backend whose archive primitives actually work ---------
    #
    # No bind mount here on purpose: a client-local bind needs root on these targets to
    # kernel-9p-mount, which is why every bind scenario in this suite is kube-only. What
    # is under test is the OTHER route — bytes through the BFF, framed as a tar, into and
    # out of the real backend's CopyTo/CopyFrom/StatPath/Exec.
    proj = temp_dir()
    compose_file = proj + "/compose.yaml"
    # Two named volumes. Until 2026-08-08 this arm declared none, so the BFF's
    # fsop probe (which looks for a volume-backed path) could never succeed and the
    # host backends' structured-operation route was unreachable from this scenario
    # even after they implemented it. They are what the server-route arm below needs.
    write_file(path = compose_file, content = compose_yaml("alpine:3.20", """    volumes:
      - vol1:/vol1
      - vol2:/vol2
volumes:
  vol1:
  vol2:
"""))

    addr = serve()
    srv = "http://" + addr
    compose_up(file = compose_file, project = project, detach = True)
    wait(name = app, running = 1, timeout = "300s")
    base = web(compose_file = compose_file, project = project)

    def in_container(cmd):
        return exec_tty(argv = ["cornus", "exec", "--server", srv, app, "sh", "-c", cmd])["output"]

    def fs_post(rel, path, body):
        return http(
            method = "POST",
            url = base + "/.cornus/web/fs/" + rel + "?source=virtual&path=" + path,
            body = body,
            headers = {"Content-Type": "application/json"},
        )

    # A container path lists through the real backend.
    lst = http_get(url = base + "/.cornus/web/fs?source=virtual&path=" + app + "/etc")
    assert_eq(lst["status"], 200, "listing a container path: %s" % lst["body"])
    assert_contains(lst["body"], "hostname", "/etc should hold hostname")

    # Streaming OUT of a container. This is the direction the unit suite could not see:
    # it owned every failure mode and never the success, so a copy that completed and
    # then wedged on a second Close looked green for as long as nobody ran one.
    in_container("mkdir -p /work && echo FROM-THE-IMAGE > /work/seed.txt")
    out = fs_post("copy", app + "/work/seed.txt", '{"to":"project/seed.txt"}')
    assert_eq(out["status"], 200, "copying a file OUT of the container: %s" % out["body"])
    assert_contains(read_file(path = proj + "/seed.txt"), "FROM-THE-IMAGE",
                    "the copy out of the container did not deliver the bytes")
    log("✓ a file streamed out of the container and the request returned")

    # 11 MB is past the editor's 10 MB bound on purpose: that bound used to apply to
    # every transfer, so a file this size is exactly the one that could not move at all.
    # Random content and a checksum, because a zero-filled file compares equal to a
    # truncated zero-filled file.
    big = proj + "/big.bin"
    sh(cmd = "head -c 11000000 /dev/urandom > %s" % big)
    want = sh(cmd = "sha256sum %s | cut -d' ' -f1" % big)["output"]

    cp = fs_post("copy", "project/big.bin", '{"to":"%s/work/big.bin"}' % app)
    assert_eq(cp["status"], 200, "relaying an 11 MB file into the container: %s" % cp["body"])
    assert_contains(in_container("sha256sum /work/big.bin | cut -d' ' -f1"), want,
                    "the relayed file is not byte-for-byte (want %s)" % want)

    rt = fs_post("copy", app + "/work/big.bin", '{"to":"project/back.bin"}')
    assert_eq(rt["status"], 200, "streaming the 11 MB file back out: %s" % rt["body"])
    assert_eq(sh(cmd = "sha256sum %s/back.bin | cut -d' ' -f1" % proj)["output"], want,
              "the round trip through the container is not byte-for-byte")
    log("✓ 11 MB relayed in and back out, byte for byte — the old cap was 10 MB")

    # An upload is the same gesture from the other side of the window, and used to be the
    # one that 413'd where a copy succeeded.
    up = http(
        method = "POST",
        url = base + "/.cornus/web/fs/upload?source=virtual&path=" + app + "/work&name=uploaded.txt",
        body = "UPLOADED-THROUGH-THE-RELAY\n",
    )
    assert_eq(up["status"], 200, "upload into a container path: %s" % up["body"])
    assert_contains(in_container("cat /work/uploaded.txt"), "UPLOADED-THROUGH-THE-RELAY",
                    "the upload did not reach the container")

    # A move is one request, and the source really goes. On this route it ends in the
    # legacy in-container `mv`, whose exit status the BFF used to be unable to read at
    # all — a failed move reported success, which is only destructive once something
    # deletes the source.
    mv = fs_post("move", app + "/work/uploaded.txt", '{"to":"%s/work/moved.txt"}' % app)
    assert_eq(mv["status"], 200, "moving within the container: %s" % mv["body"])
    assert_contains(in_container("test -e /work/uploaded.txt && echo STILL-THERE || echo GONE"), "GONE",
                    "a move left its source behind")
    assert_contains(in_container("cat /work/moved.txt"), "UPLOADED-THROUGH-THE-RELAY",
                    "the moved file did not arrive")
    log("✓ move landed the destination and removed the source, as the container sees it")

    # H6, against a backend that may ignore the option. NoOverwriteDirNonDir is
    # best-effort — barehost's gVisor tar-exec path and incus drop it — so the BFF
    # pre-checks with StatPath. Writing a file over a same-named DIRECTORY used to run
    # os.RemoveAll on the tree.
    in_container("mkdir -p /work/adir && echo KEEP > /work/adir/inside.txt")
    clash = http(
        method = "PUT",
        url = base + "/.cornus/web/fs/content?source=virtual&path=" + app + "/work/adir",
        body = "one byte\n",
    )
    assert_true(clash["status"] != 200,
                "writing a file over an existing DIRECTORY must be refused, got %d" % clash["status"])
    assert_contains(in_container("cat /work/adir/inside.txt"), "KEEP",
                    "the directory tree was destroyed by a file write — this is the H6 regression")
    log("✓ a file write over an existing directory was refused and the tree survived")

    # Preflight describes the route it will really take. `relay` is the honest answer on
    # this backend: nothing here is backed by a client-local bind.
    pre = http(
        method = "POST",
        url = base + "/.cornus/web/fs/preflight?op=copy&source=virtual&path=project",
        body = '{"to":"%s/work","items":[{"from":"project/seed.txt","to":"%s/work/pre.txt"}]}' % (app, app),
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(pre["status"], 200, "preflight: %s" % pre["body"])
    item = json.decode(pre["body"])["items"][0]
    assert_eq(item["route"], "relay",
              "a container path with no bind behind it must route through the relay, got %r" % item["route"])
    assert_eq(item["files"], 1, "preflight should account for the one file it would move")

    # ---- the server route on a host backend --------------------------------------
    #
    # A copy WITHIN one workload, volume to volume. This is what deploy.FSOperator
    # buys on a host backend: the server reaches the container's rootfs through
    # /proc/<pid>/root and renames or copies in place, instead of dragging every
    # byte out to the BFF and back. The kube arm asserts the same thing through the
    # caretaker; this is the host counterpart, and until the backends implemented
    # FSOp there was nothing to assert.
    #
    # It is PRIVILEGE-DEPENDENT, and that is the interesting half. Entering another
    # process's rootfs needs root: a container's init is root-owned, so an ordinary
    # `cornus serve` cannot read /proc/<pid>/root and the operator answers
    # "unsupported" — on purpose, so the caller relays and the copy still works.
    # Asserting one route unconditionally would therefore fail on whichever host the
    # suite did not happen to run on.
    in_container("mkdir -p /vol1 /vol2 && echo IN-A-VOLUME > /vol1/seed.txt")
    vpre = http(
        method = "POST",
        url = base + "/.cornus/web/fs/preflight?op=copy&source=virtual&path=" + app + "/vol1",
        body = '{"to":"%s/vol2","items":[{"from":"%s/vol1/seed.txt","to":"%s/vol2/pre.txt"}]}' % (app, app, app),
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(vpre["status"], 200, "volume preflight: %s" % vpre["body"])
    vitem = json.decode(vpre["body"])["items"][0]
    uid = sh(cmd = "id -u")["output"].strip()
    if uid == "0":
        assert_eq(vitem["route"], "server",
                  "a same-workload volume-to-volume copy must run server-side once the backend " +
                  "implements deploy.FSOperator, got route=%r why=%r" % (vitem["route"], vitem.get("why", "")))
        assert_true(vitem["native"], "a server-side copy should be native (no bytes move)")
        log("✓ the host backend served the copy structurally, and preflight declared it")
    else:
        assert_eq(vitem["route"], "relay",
                  "an unprivileged server cannot read a root-owned container's /proc/<pid>/root, " +
                  "so it must fall back to the relay rather than fail, got route=%r" % vitem["route"])
        log("! unprivileged server: the structured route is unavailable and the relay took it, as designed")

    # Whichever route it took, the bytes must land — the route is an optimization
    # and must never be the difference between working and not.
    vcp = fs_post("copy", app + "/vol1", '{"to":"%s/vol2","items":[{"from":"%s/vol1/seed.txt","to":"%s/vol2/copied.txt"}]}' % (app, app, app))
    assert_eq(vcp["status"], 200, "volume-to-volume copy: %s" % vcp["body"])
    assert_contains(in_container("cat /vol2/copied.txt"), "IN-A-VOLUME",
                    "the volume-to-volume copy did not deliver the bytes")
    log("✓ the volume-to-volume copy landed whichever route it took")


    compose_down(file = compose_file, project = project)
    # Drop the 11 MB fixtures but KEEP the compose file: the harness reaps scenario
    # workloads with a second `compose down` after the body, and deleting the file out
    # from under it turns that redundant call into a noisy error.
    remove_all(path = big)
    remove_all(path = proj + "/back.bin")
    log("✓ web BFF file explorer verified against the live %s backend (relay arm)" % TARGET)
