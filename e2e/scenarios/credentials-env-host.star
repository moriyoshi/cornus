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

    # Every target here delivers FILE credentials, and the two that remap ids get
    # there by different routes. Rootless podman: the server owns the file as the
    # ids the WORKLOAD runs with, translated through the daemon's own id map
    # (deploy.IDMapper), which is knowable before the container exists.
    #
    # incus is the one where it is not. Its map lives on the INSTANCE, so there is
    # nothing to ask when the server writes the file; the backend creates the
    # instance STOPPED, takes ownership of the server's directory once the map
    # exists, and starts it afterwards (deploy.LateIDCredentialBinder).
    #
    # So there is no skip left in the file arm below. A self-skip that outlives its
    # cause is indistinguishable from coverage, and this one did outlive it.
    supports_file = True
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
        fail(msg = "supports_file is False, but every target this scenario runs on delivers files; " +
                   "if a target regresses, say which and why rather than skipping silently")
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
        # The workload records its OWN uid and its OWN read of the credential
        # before settling, so the assertion below observes the process the spec
        # configured rather than whatever an exec happens to run as. It retries:
        # the credential arrives with the container, but a workload racing its own
        # first read would make this flaky for a reason unrelated to ownership.
        deploy_attach(
            name = "creds-nonroot",
            image = "alpine:3.20",
            # entrypoint, NOT command: incus can only replace the whole argv, so a
            # command-only override is ignored there with a warning — and this
            # workload's whole job is the argv it was given.
            entrypoint = ["sh", "-c",
                       "for i in $(seq 1 60); do " +
                       "  if [ -r /creds/api.json ]; then " +
                       "    (echo \"uid=$(id -u)\"; cat /creds/api.json) > /tmp/proof 2>&1; break; " +
                       "  fi; sleep 1; " +
                       "done; " +
                       "[ -s /tmp/proof ] || echo \"uid=$(id -u) UNREADABLE $(ls -ln /creds 2>&1)\" > /tmp/proof; " +
                       "sleep 3600"],
            credentials_json = creds2,
            user = "1000:1000",
            timeout = "240s",
        )
        wait(name = "creds-nonroot", running = 1, timeout = "240s")

        # The WORKLOAD does the reading, not an exec. On incus `cornus exec` runs
        # as root whatever the instance's init uid is (measured: oci.uid=1000 puts
        # PID 1 on host uid 1001000 while `incus exec -- id -u` still answers 0),
        # so an exec-based read proves nothing about a non-root workload there —
        # and the `id -u` precondition proved something about the exec instead.
        #
        # The command above already wrote its own uid and its own read of the
        # credential into /tmp/proof, as the user the spec asked for. Exec is used
        # only to fetch that result, which it may do as anyone.
        proof = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "creds-nonroot", "sh", "-c", "cat /tmp/proof"])
        assert_contains(proof["output"], "uid=1000",
                        "the workload was not running as uid 1000, so this arm proves nothing about " +
                        "a non-root read")
        assert_contains(proof["output"], "k-9876",
                        "a non-root workload could not read its credential file; the server owned it " +
                        "as an id this workload's user does not map to")
        log("✓ a non-root workload read its own credential file")

    st = status(name = "creds-env")
    assert_eq(st["running"], 1, "Status must report exactly the app instance")

    # 5) The credential is scoped to the session that declared it: the spec's
    #    credential block is cleared once realized, so the backend must not warn
    #    that it ignored a credential it in fact delivered.
    logs = server_log()
    if "sees none of the declared credentials" in logs:
        fail(msg = "the backend warned it ignored a credential that was delivered")
    log("✓ no false 'credentials ignored' warning in the server log")
