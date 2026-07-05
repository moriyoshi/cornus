# Agent CLASSIFICATION end to end: is a session whose foreground program is an
# agent classified from THAT agent's manifest, and does an identical screen in a
# non-agent session stay quiet?
#
# web-terminal-introspect.star proves the pid/cwd plumbing. This proves the half
# that plumbing exists to feed: identification through the manifest set
# (pkg/agentdetect) and classification by the vendored manifest for the identified
# agent (third_party/herdr/manifests/claude.toml).
#
# BOTH halves must hold, and the negative is the one that matters. The same words
# are a prompt when the program showing them is prompting and are just text when
# it is not — a detector that pattern-matched the screen alone passes the positive
# case and fails the negative, which is precisely the defect this design replaced.
#
# Opt-in (make e2e-web-agent); not in the default SCENARIOS list.

if TARGET == "local":
    log("agent classification: skipped (needs a real backend)")
else:
    serve()

    deploy(name = "wagent", image = "alpine:3.20", command = ["sleep", "3600"])
    wait(name = "wagent", running = 1, timeout = "240s")

    base = web()

    # The screen both sessions show — a shape the bundled claude manifest
    # classifies as blocked. Written with plain `echo` and single quotes ONLY: the
    # command travels through a JSON body, and every backslash or double quote it
    # avoids is one less escaping layer for a reader to unpick.
    SHOW = "echo 'Do you want to proceed?'; echo '❯ 1. Yes'; echo '  2. No'"

    # How a session becomes "an agent" without installing one, and why it is
    # launched THROUGH the interpreter explicitly.
    #
    # /tmp/claude is a script. Running it directly (`exec /tmp/claude`) would NOT
    # exercise the interesting path: Linux sets `comm` from the script's own name,
    # so comm is already "claude" and a comm-only reader finds it. Measured — with
    # runtime unwrapping disabled the scenario still passed, which made the
    # positive half vacuous.
    #
    # `exec /bin/sh /tmp/claude` puts the INTERPRETER in comm ("sh") and leaves the
    # agent's name only in argv, which is the shape a real Node- or Python-based
    # agent presents and the one comm alone cannot see.
    #
    # The script must NOT exec its sleep: leaving sleep as a child keeps the script
    # shell itself in the foreground, which is what carries the agent name. And the
    # image's busybox cannot simply be copied under a new name — busybox dispatches
    # on argv[0], so a renamed copy is an unknown applet.
    AGENT = SHOW + "; echo '#!/bin/sh' > /tmp/claude; echo 'sleep 300' >> /tmp/claude" + \
            "; chmod +x /tmp/claude; exec /bin/sh /tmp/claude"
    PLAIN = SHOW + "; sleep 300"

    # --- the agent session -------------------------------------------------------
    created = http(
        method = "POST",
        url = base + "/.cornus/web/terminals",
        body = '{"workload":"wagent","cmd":["/bin/sh","-c","' + AGENT + '"]}',
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(created["status"], 200, "create the agent session")

    body = ""
    for i in range(80):
        listed = http_get(url = base + "/.cornus/web/terminals")
        assert_eq(listed["status"], 200, "list terminal sessions")
        body = listed["body"]
        if body.find('"agent":"claude"') != -1 and body.find('"state":"blocked"') != -1:
            break
        sleep(duration = "500ms")

    assert_contains(
        body,
        '"agent":"claude"',
        "the foreground program was never identified as an agent — the chain is pid announcement, /proc tpgid, /proc cmdline, then runtime unwrapping, and any link can be the one that broke",
    )
    assert_contains(
        body,
        '"state":"blocked"',
        "an agent session showing an approval prompt was not classified blocked by its own manifest",
    )
    log("✓ agent identified through its interpreter and classified blocked by its bundled manifest")

    # --- the negative: same screen, ordinary shell -------------------------------
    created2 = http(
        method = "POST",
        url = base + "/.cornus/web/terminals",
        body = '{"workload":"wagent","cmd":["/bin/sh","-c","' + PLAIN + '"]}',
        headers = {"Content-Type": "application/json"},
    )
    assert_eq(created2["status"], 200, "create the plain shell session")

    body2 = ""
    for i in range(80):
        listed2 = http_get(url = base + "/.cornus/web/terminals")
        assert_eq(listed2["status"], 200, "list terminal sessions")
        body2 = listed2["body"]
        # Wait for the second session to have been classified at all, so the count
        # below is taken after it settled rather than before it was looked at.
        if body2.count('"state":') >= 2:
            break
        sleep(duration = "500ms")

    blocked = body2.count('"state":"blocked"')
    assert_eq(
        blocked,
        1,
        "%d sessions classified blocked, want exactly 1 — a shell merely DISPLAYING an agent's prompt text must not be classified as prompting. Body: %s" % (blocked, body2),
    )
    assert_eq(
        body2.count('"agent":"claude"'),
        1,
        "the shell session was also identified as an agent; identification must read the foreground program, not the /bin/sh launch argv both sessions share. Body: %s" % body2,
    )
    log("✓ a shell showing the identical prompt text is NOT classified — the scoping holds")
