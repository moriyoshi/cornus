# Following the flight record live, and reading it from an agent.
#
# The one-shot read is covered by activity-flight-record.star; this covers the
# two surfaces layered on top of it, and both need a REAL server because both are
# about transport rather than about the records:
#
#   - `cornus activity --follow` streams Server-Sent Events over the wire. A unit
#     test can prove the framing, but not that the bytes reach a separate process
#     before the handler returns, that filters survive the round trip, or that
#     Ctrl-C is a clean exit. Those are the whole point of a follow.
#   - the MCP surface is what an agent uses. mcp-stdio-protocol.star proves the
#     tool and the resource are listed; what needs a scenario with real history
#     is that they carry the right records — this run's deploy present in the
#     flight, and absent from the unfinished set because it completed.
#
# Docker-only: needs a deployable workload to make something happen while the
# stream is open.

if TARGET != "docker":
    log("activity-follow: skipped (docker-only; needs a live deploy to record while following)")
else:
    addr = serve()
    work = temp_dir()

    # --- follow: history, then live -----------------------------------------

    # A deploy started AFTER the stream is up. It has to be a name no earlier run
    # left behind, or its record could be matched out of the backlog and the test
    # would pass without ever proving live delivery.
    sh(cmd = "docker rm -f cornus-flightfollow-0 >/dev/null 2>&1 || true")
    spec = work + "/follow.yaml"
    write_file(
        path = spec,
        content = 'name: flightfollow\nimage: alpine:3.20\ncommand: ["sh", "-c", "sleep infinity"]\n',
    )

    followed = cornus_stream(
        "activity", "--server", "http://" + addr, "--follow",
        trigger = ["deploy", "-f", spec, "--server", "http://" + addr, "--no-forward-ports"],
        until = "flightfollow",
        timeout = "180s",
    )
    # cornus_stream fails the scenario if `until` never arrives, so reaching here
    # already means the record crossed the wire while the stream was open. Assert
    # the shape too: a live line names the process that wrote it, because grouping
    # by incarnation is impossible before a run has ended.
    assert_contains(followed["output"], "deploy", "the followed stream must carry the deploy record")
    assert_contains(followed["output"], "server/", "each live line must name the process and instance that wrote it")
    # Ctrl-C is how a follow ends. Reporting failure would make it unusable in a
    # script and alarming by hand.
    assert_eq(followed["code"], 0, "interrupting --follow must be a clean exit")
    log("✓ --follow delivered a record written while it was streaming, and exited 0 on Ctrl-C")

    # A filter that excludes everything must yield a stream that stays silent
    # rather than quietly falling back to everything. Prove it by asking for a
    # kind this run cannot produce and checking the deploy does NOT appear.
    filtered = cornus_stream(
        "activity", "--server", "http://" + addr, "--follow", "--kind", "server",
        until = "server",
        timeout = "60s",
    )
    assert_eq(filtered["output"].find("deploy "), -1, "--kind server must not pass deploy records through")
    log("✓ --follow honors --kind on the live stream")

    # --unfinished is a question about the whole stream, so it cannot be a feed.
    # Refusing beats serving a view that lies as it goes.
    refused = cornus("activity", "--server", "http://" + addr, "--follow", "--unfinished", expect_fail = True)
    assert_contains(refused, "snapshot", "--follow --unfinished must be refused, with the reason")
    log("✓ --follow --unfinished is refused")

    # --- MCP: the same records, as an agent gets them -----------------------

    # mcp-stdio-protocol.star already proves activity_read and the unfinished
    # resource are LISTED. What is left, and what needs this scenario's history to
    # assert at all, is that they carry the right records: the deploy above is in
    # the flight, and — because it ran to completion and was torn down — it is NOT
    # in the unfinished set. That pairing is the recorder's whole contract.
    session = mcp_stdio()
    handle = session["handle"]

    read = mcp_call(handle = handle, tool = "activity_read")
    assert_eq(read["is_error"], False, "activity_read")
    assert_contains(read["text"], "flightfollow", "activity_read must return this run's deploy record")
    # Without liveInstance an agent cannot tell the serving process's own open
    # lifetime from a crashed one, and would report a healthy server as dead.
    assert_true(read["value"]["liveInstance"] != "", "activity_read must name the live incarnation")
    log("✓ MCP activity_read returned this run's flight")

    unfinished = mcp_read_resource(handle = handle, uri = "cornus://activity/unfinished")
    assert_eq(unfinished["is_error"], False, "resources/read")
    # The serving server's own lifetime is open by definition, so this set is
    # never empty while it is running — which is exactly why liveInstance has to
    # travel with it, or an agent reads a healthy server as a crashed one.
    assert_true(unfinished["value"]["liveInstance"] != "", "the unfinished set must name the live incarnation")
    kinds = [e["kind"] for e in unfinished["value"]["events"]]
    assert_true("server" in kinds, "the live server's own open lifetime must be in the unfinished set")
    # The deploy began AND ended, so reporting it here would mean the unfinished
    # set never converges — every completed operation would accumulate as a
    # permanent phantom incident.
    assert_eq(unfinished["text"].find("flightfollow"), -1,
              "a deploy that ran to completion must not be reported as unfinished")
    log("✓ MCP cornus://activity/unfinished held the open lifetime and not the completed deploy")

    mcp_close(handle = handle)

    # The workload outlived its deploy session only if teardown lagged; make sure
    # the next run starts clean either way.
    sh(cmd = "docker rm -f cornus-flightfollow-0 >/dev/null 2>&1 || true")
