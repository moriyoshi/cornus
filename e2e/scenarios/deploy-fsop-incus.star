# deploy.FSOperator on incus, served over the daemon's own SFTP channel.
#
# web-fs.star is the general file-explorer scenario, but it cannot reach the
# operator section on incus: its fixture uses managed volumes, cornus creates those
# with `security.shifted: true` (deliberately — replicas of one deployment need not
# map ids the same way), and a shifted volume needs IDMAPPED MOUNTS, which the
# containerized runner's kernel does not provide. Measured, not assumed: with the
# create/start split reverted the same failure appears at create instead of start,
# so it is the environment and not cornus's ordering.
#
# This scenario needs no volume. Unlike the caretaker, which maps each mounted
# volume separately, incus's operator serves ONE root — Target "/" over the
# instance's whole filesystem — so any path is reachable.
#
# What it pins is the half unit tests cannot: that a real incusd opens the channel,
# and that RENAME works across it. Rename is close to the reason FSOperator exists
# at all — the archive trio can already stat and copy, and cannot move anything —
# and it is the one operation the instance FILE API this backend uses for `cp`
# cannot express, which is why the SFTP channel was chosen over it.

if TARGET != "incus":
    log("deploy-fsop-incus: skipped (incus only; the backend whose operator rides the SFTP channel)")
else:
    addr = serve()
    app = "fsopinc"
    deploy(
        name = app,
        image = "alpine:3.20",
        # entrypoint, not command: incus replaces the whole argv, so a command-only
        # override is ignored there with a warning.
        entrypoint = ["sh", "-c", "mkdir -p /srv/data && echo FSOP-PAYLOAD > /srv/data/a.txt && sleep 3600"],
    )
    wait(name = app, running = 1, timeout = "240s")

    base = "http://" + addr + "/.cornus/v1/deploy/" + app + "/fsop"

    # 1) STAT. A backend with no operator answers `unsupported` here and the caller
    #    relays instead, so a 200 with a real stat is what says the channel opened.
    st = http(method = "POST", url = base + "?op=stat&path=/srv/data/a.txt")
    if st["status"] != 200:
        fail(msg = "fsop stat: %d %s" % (st["status"], st["body"]))
    if "unsupported" in st["body"]:
        fail(msg = "the operator declined to serve an instance path: %s — the SFTP channel did not open, " % st["body"] +
                   "so every explorer operation falls back to relaying every byte")
    assert_contains(st["body"], "a.txt", "stat must name the file")
    log("✓ stat served over the instance's SFTP channel")

    # 2) LIST — the operation the archive primitives cannot express at all.
    ls = http(method = "POST", url = base + "?op=list&path=/srv/data")
    if ls["status"] != 200:
        fail(msg = "fsop list: %d %s" % (ls["status"], ls["body"]))
    assert_contains(ls["body"], "a.txt", "the listing must contain the file")
    log("✓ list served structurally, with no tar relay")

    # 3) RENAME, in place. This is the headline: no byte crosses to the caller, and
    #    the instance FILE API has no rename at all, so a regression to that route
    #    would fail here rather than silently costing a full round trip.
    mv = http(method = "POST", url = base + "?op=rename&path=/srv/data/a.txt&to=/srv/data/b.txt")
    if mv["status"] != 200:
        fail(msg = "fsop rename: %d %s" % (mv["status"], mv["body"]))

    # It really moved: the new name is there and the old one is gone. Asserting only
    # the 200 would pass against an operator that reported success and did nothing.
    after = http(method = "POST", url = base + "?op=list&path=/srv/data")
    assert_contains(after["body"], "b.txt", "the renamed file must exist under its new name")
    if "a.txt" in after["body"]:
        fail(msg = "the source name is still present after a rename: %s" % after["body"])

    # And the BYTES survived, which a rename that recreated an empty file would not
    # show in a listing.
    cat = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, app, "sh", "-c", "cat /srv/data/b.txt"])
    assert_contains(cat["output"], "FSOP-PAYLOAD", "the renamed file's contents must survive")
    log("✓ in-place rename over SFTP: moved, old name gone, bytes intact")

    # 4) MKDIR + REMOVE, so the write path is exercised in both directions.
    mk = http(method = "POST", url = base + "?op=mkdir&path=/srv/data/sub")
    if mk["status"] != 200:
        fail(msg = "fsop mkdir: %d %s" % (mk["status"], mk["body"]))
    rm = http(method = "POST", url = base + "?op=remove&path=/srv/data/sub&recursive=true")
    if rm["status"] != 200:
        fail(msg = "fsop remove: %d %s" % (rm["status"], rm["body"]))
    gone = http(method = "POST", url = base + "?op=list&path=/srv/data")
    if "sub" in gone["body"]:
        fail(msg = "the directory is still listed after a recursive remove: %s" % gone["body"])
    log("✓ mkdir and recursive remove served over the channel")

    remove(name = app)
