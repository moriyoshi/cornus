# Client-sourced credentials delivered into a container by the SERVER ITSELF, on
# the host backends — no caretaker companion anywhere.
#
# Two kinds need nothing running beside the workload. An `env` value is minted on
# the CLIENT (here the zero-dependency `static` source), fetched once by the
# server over the held deploy-attach session, and set in the container's
# environment at create. A `file` is rendered by the server under its own mounts
# dir and bound read-only into the workload, refreshed by swapping a symlink. So
# the whole caretaker apparatus — the companion container, CORNUS_ADVERTISE_URL,
# CORNUS_AGENT_IMAGE — is not merely unnecessary but absent.
#
# That absence is the regression this scenario exists for. A credential-bearing
# deploy on a host backend used to be refused with
#   "... require CORNUS_ADVERTISE_URL (the cornus URL the caretaker dials back on)"
# because the dispatch keyed on "are there credentials" rather than "does anything
# here dial back". serve() below deliberately sets NEITHER variable, so a
# regression fails at deploy rather than in an assertion.
#
# Host-target counterpart of credentials.star (kube-only, caretaker-delivered).
# pod_exec is kube-only, so readback goes through `cornus exec`, which every
# target supports.
#
# Source of truth: pkg/credential (client source), pkg/deploy/credential_env.go
# (RealizeCredentials), pkg/server/credential_files.go (render + materialize),
# pkg/server/deploy_attach.go (the dispatch).

creds = '[{"name":"db","backend":"static",' + \
        '"config":{"value":"s3cr3t-env-value"},' + \
        '"deliveries":[{"kind":"env","envVar":"DB_TOKEN"}]}]'

# A second source proves multiple credentials land, that a non-default valueKey is
# honored rather than falling back to "value"/"token", and that a file delivery
# rides alongside the env ones.
creds2 = '[{"name":"db","backend":"static",' + \
         '"config":{"value":"s3cr3t-env-value"},' + \
         '"deliveries":[{"kind":"env","envVar":"DB_TOKEN"}]},' + \
         '{"name":"api","backend":"static",' + \
         '"config":{"apikey":"k-9876"},' + \
         '"deliveries":[{"kind":"env","envVar":"API_KEY","valueKey":"apikey"},' + \
         '{"kind":"file","path":"/creds/api.json","format":"json"}]}]'

# Same two sources with the FILE delivery dropped, for a target that cannot take
# one. It keeps both env deliveries — including the non-default valueKey — so the
# arms that are supported are still asserted rather than skipped wholesale.
creds2_envonly = '[{"name":"db","backend":"static",' + \
                 '"config":{"value":"s3cr3t-env-value"},' + \
                 '"deliveries":[{"kind":"env","envVar":"DB_TOKEN"}]},' + \
                 '{"name":"api","backend":"static",' + \
                 '"config":{"apikey":"k-9876"},' + \
                 '"deliveries":[{"kind":"env","envVar":"API_KEY","valueKey":"apikey"}]}]'

if TARGET not in ["docker", "podman", "podman-rootless", "bare", "containerd", "incus"]:
    log("credentials-env-host: skipped (host-backend scenario; kube delivery is covered by credentials.star)")
else:
    # No CORNUS_ADVERTISE_URL, no CORNUS_AGENT_IMAGE. If the server asks for
    # either, the deploy below fails and this scenario is doing its job.
    addr = serve()

    # Rootless podman cannot take a FILE delivery: the daemon runs as an ordinary
    # user and cannot traverse a credential directory this server owns, and the
    # 0600 file inside it would be unreadable by the user the container's root
    # maps to. cornus refuses it by name rather than letting podman fail with a
    # statfs error. env still works, so this exercises what the target supports.
    # Two targets cannot take a FILE delivery, for the same underlying reason: the
    # runtime cannot read a file this server owns. Rootless podman runs as an
    # ordinary user and cannot even traverse the credential directory; incus
    # idmap-shifts a host disk device, so the 0600 file arrives owned by nobody.
    # cornus refuses that kind by name on both. env still works, so this exercises
    # what each target actually supports rather than skipping wholesale.
    # Rootless podman now takes a file delivery: the server owns the file as the
    # ids the WORKLOAD runs with, translated through the daemon's own id map
    # (deploy.IDMapper). incus still cannot, and no longer for the mapping
    # reason — it records its map on the INSTANCE, which does not exist when the
    # file has to be written.
    supports_file = TARGET != "incus"
    deploy_attach(
        name = "creds-env",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        credentials_json = creds2 if supports_file else creds2_envonly,
        timeout = "240s",
    )
    wait(name = "creds-env", running = 1, timeout = "240s")
    log("✓ credential workload is up with no advertise URL and no agent image configured")

    # 1) The value the client minted is in the container's environment.
    out = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-env", "sh", "-c", "printenv DB_TOKEN"])
    assert_contains(out["output"], "s3cr3t-env-value", "the env delivery must reach the container's environment")
    log("✓ read the credential from $DB_TOKEN inside the container")

    # 2) valueKey selects a non-default key from the credential's values.
    out2 = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-env", "sh", "-c", "printenv API_KEY"])
    assert_contains(out2["output"], "k-9876", "valueKey must select the named value, not the 'value'/'token' fallback")
    log("✓ a second source with an explicit valueKey landed too")

    # 3) The point of the whole path: NOT ONE companion was created. Status counts
    #    only app instances, so it cannot see a companion either way — assert on
    #    the runtime's own container list instead.
    if TARGET == "docker":
        lst = sh(cmd = "docker ps --format '{{.Names}}' --filter label=cornus.app=creds-env")
        assert_contains(lst["output"], "cornus-creds-env-0", "the app instance must be running")
        if "caretaker" in lst["output"] or "mount-0" in lst["output"] or "egress-0" in lst["output"]:
            fail(msg = "a caretaker companion was created for an env-only credential: %r" % lst["output"])
        log("✓ no caretaker companion beside the app")
    elif TARGET == "bare":
        lst = sh(cmd = "runc --root /run/cornus/bare-runc list -q 2>/dev/null")
        assert_contains(lst["output"], "cornus-creds-env-0", "the app instance must be running")
        if "caretaker" in lst["output"] or "creds-env-mount" in lst["output"] or "creds-env-egress" in lst["output"]:
            fail(msg = "a caretaker companion was created for an env-only credential: %r" % lst["output"])
        log("✓ no caretaker companion beside the app")

    # 4) The file delivery: rendered by the server, bound read-only, and visible
    #    to the workload as a plain path rather than the versioned machinery
    #    behind it.
    if not supports_file:
        why = "this target's runtime cannot read a file this server owns"
        if TARGET == "incus":
            why = "incus records its id map on the INSTANCE, which does not exist yet when the credential file has to be written"
        log("skipping the file-delivery assertions: %s, so cornus refuses that kind by name and the deploy above declares only env deliveries" % why)
    else:
        filed = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-env", "sh", "-c", "cat /creds/api.json"])
        assert_contains(filed["output"], "k-9876", "the file delivery must be readable at its declared path")

        # The versioned directories behind it are dot-dot-prefixed, so a plain `ls`
        # shows the credential and nothing else — an app that iterates the directory
        # must not trip over the machinery.
        listing = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-env", "sh", "-c", "ls /creds"])
        assert_contains(listing["output"], "api.json", "the credential file must be listed")
        if "..v" in listing["output"] or "..data" in listing["output"]:
            fail(msg = "the atomic-write machinery leaked into the workload's listing: %r" % listing["output"])
        log("✓ read the credential file at /creds/api.json; version dirs stay hidden")

    # A NON-ROOT workload reading a file delivery. This is the arm that proves the
    # id mapping rather than assuming it: on a runtime that remaps, the file must
    # be owned by the host id that THIS user maps to (podman: container 1000 ->
    # host 100999), and owning it as the range base — container root — leaves it
    # exactly as unreadable as leaving it untranslated.
    #
    # It runs everywhere the file arm does, because the assertion is equally
    # meaningful without remapping: there the ids pass through unchanged and a
    # 0600 file owned by 1000 is still only readable by 1000.
    if supports_file:
        deploy_attach(
            name = "creds-nonroot",
            image = "alpine:3.20",
            command = ["sleep", "3600"],
            credentials_json = creds2,
            user = "1000:1000",
            timeout = "240s",
        )
        wait(name = "creds-nonroot", running = 1, timeout = "240s")
        who = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-nonroot", "sh", "-c", "id -u"])
        assert_contains(who["output"], "1000", "the workload must actually be running as uid 1000, or this arm proves nothing")
        nr = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-nonroot", "sh", "-c", "cat /creds/api.json"])
        assert_contains(nr["output"], "k-9876",
                        "a non-root workload could not read its credential file; the server owned it " +
                        "as an id this workload's user does not map to")
        log("✓ a non-root workload read its credential file")

    st = status(name = "creds-env")
    assert_eq(st["running"], 1, "Status must report exactly the app instance")

    # 5) The credential is scoped to the session that declared it: the spec's
    #    credential block is cleared once realized, so the backend must not warn
    #    that it ignored a credential it in fact delivered.
    logs = server_log()
    if "sees none of the declared credentials" in logs:
        fail(msg = "the backend warned it ignored a credential that was delivered")
    log("✓ no false 'credentials ignored' warning in the server log")
