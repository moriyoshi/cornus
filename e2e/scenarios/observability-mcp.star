# The observability MCP surface, over the real stdio transport.
#
# The unit tests connect an in-memory MCP client to the BFF and assert both the
# behavior and the tool DESCRIPTIONS (a model picks tools by matching the user's
# situation against that prose, so a tool that fails to say its logs outlive the
# container is one it will never reach for at the only moment that matters).
#
# What only a live run proves is that those tools are actually reachable through
# `cornus web --mcp-stdio` against a real server with a real store — that the
# whole chain from an agent's tool call to a recorded log line composes. The
# failure this guards against is the tools existing but every call erroring out
# because the BFF's client cannot reach the store's routes.
#
# Source of truth: cmd/cornus/internal/webbff/observe.go (core + HTTP),
# mcp.go (registerMCPObserveTools, the errors resource).

if TARGET == "local":
    log("skip observability-mcp: needs a running server + deploy backend")
else:
    srv = serve(env = {"CORNUS_OBS": "1"})
    base = "http://" + srv

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("skip observability-mcp: this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        marker = "cornus-mcp-obs-marker"
        work = temp_dir()
        compose_file = work + "/compose.yaml"
        write_file(
            path = compose_file,
            content = """services:
  web:
    image: busybox:latest
    command: ["sh", "-c", "echo %s; sleep 60"]
""" % marker,
        )
        compose_up(file = compose_file, project = "obsmcp", detach = True)

        session = mcp_stdio(compose_file = compose_file, project = "obsmcp")
        handle = session["handle"]

        # --- the tools are advertised -----------------------------------------
        tools = mcp_list_tools(handle)
        for want in ["observe_logs", "observe_traces", "observe_trace", "observe_metrics", "observe_status"]:
            assert_true(want in tools, "MCP tool %s is not advertised; got %r" % (want, tools))
        log("✓ every observe_* tool is advertised over stdio")

        # --- observe_logs reaches real recorded output -------------------------
        # Searched by body rather than by service: a compose service's DEPLOYMENT
        # name is its resource name, not the service key, and hard-coding the
        # mapping here would test the scenario's guess rather than the tool.
        found = {}
        for _ in range(30):
            res = mcp_call(handle, "observe_logs", {"match": marker, "limit": 50})
            assert_true(not res["is_error"], "observe_logs errored: %s" % res["text"])
            if marker in res["text"]:
                found = res
                break
            sleep("1s")
        assert_true(
            marker in found.get("text", ""),
            "observe_logs never returned the workload's recorded output",
        )
        entries = found["value"].get("entries", [])
        assert_true(len(entries) > 0, "observe_logs returned no entries array: %r" % found["value"])
        log("✓ observe_logs returns real recorded output through MCP")

        # --- observe_status carries the counter an agent must check ------------
        st = mcp_call(handle, "observe_status", {})
        assert_true(not st["is_error"], "observe_status errored: %s" % st["text"])
        assert_true(
            "dropped" in st["value"],
            "observe_status omits the dropped counter, so an agent cannot tell a quiet workload from a shed one: %r" % st["value"],
        )
        log("✓ observe_status exposes the dropped counter")

        # --- a malformed argument is a TOOL error, not a silent empty result ----
        bad = mcp_call(handle, "observe_traces", {"minDuration": "soon"})
        assert_true(bad["is_error"], "a malformed minDuration was accepted instead of erroring")
        assert_contains(bad["text"], "minDuration", "the tool error does not name the offending field")
        log("✓ a malformed tool argument is reported as a tool error")

        # --- the errors resource is context an agent can attach ----------------
        resources = mcp_list_resources(handle)
        assert_true(
            "cornus://observe/errors" in resources,
            "the workload-errors resource is not advertised; got %r" % resources,
        )
        errs = mcp_read_resource(handle, "cornus://observe/errors")
        assert_true(not errs["is_error"], "reading the errors resource failed: %s" % errs["text"])
        assert_true(
            "entries" in errs["value"],
            "the errors resource payload has no entries array: %r" % errs["value"],
        )
        log("✓ the cornus://observe/errors resource lists and reads")

        mcp_close(handle)
        compose_down(file = compose_file, project = "obsmcp")
        log("observability-mcp: all assertions passed")
