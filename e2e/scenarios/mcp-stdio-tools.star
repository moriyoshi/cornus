# MCP stdio live operations. A two-service Compose project gives the MCP BFF a
# real project graph and running workloads; logs and exec then cross every layer:
# SDK stdio client -> launched cornus web process -> BFF core -> server -> target.

if TARGET == "local":
    log("skip: MCP live tools need a deployment target")
else:
    compose_file = "e2e/scenarios/mcp-stdio-compose.yaml"
    project = "mcpe2e"

    serve()
    # Clear leftovers from a previously interrupted run before readiness checks.
    compose_down(file = compose_file)
    compose_up(file = compose_file, detach = True)
    wait(name = "mcpe2e-helper", running = 1, timeout = "180s")
    wait(name = "mcpe2e-app", running = 1, timeout = "180s")

    started = mcp_stdio(
        compose_file = compose_file,
        project = project,
        timeout = "60s",
    )
    handle = started["handle"]

    # An unexpected tool error carries its explanation in ["text"]; assert on the
    # flag but report the text, or a CI failure says only "got True, want False"
    # and the reason is gone with the run.
    def ok(res, tool):
        assert_true(not res["is_error"], "%s errored: %s" % (tool, res["text"]))

    workloads = mcp_call(handle = handle, tool = "workloads_list")
    ok(workloads, "workloads_list")
    assert_true(len(workloads["value"]["workloads"]) >= 2, "workloads_list omitted project services")
    assert_contains(workloads["text"], "mcpe2e-helper")
    assert_contains(workloads["text"], "mcpe2e-app")

    detail = mcp_call(
        handle = handle,
        tool = "workload_get",
        arguments = {"name": "mcpe2e-app"},
    )
    ok(detail, "workload_get")
    assert_eq(detail["value"]["name"], "mcpe2e-app", "workload_get resource")

    graph = mcp_call(
        handle = handle,
        tool = "project_graph",
        arguments = {"project": project},
    )
    ok(graph, "project_graph")
    assert_eq(len(graph["value"]["nodes"]), 2, "project graph node count")
    assert_eq(len(graph["value"]["edges"]), 1, "project graph edge count")
    assert_eq(graph["value"]["edges"][0]["from"], "app", "depends_on edge source")
    assert_eq(graph["value"]["edges"][0]["to"], "helper", "depends_on edge target")

    logs = mcp_call(
        handle = handle,
        tool = "logs_tail",
        arguments = {"name": "mcpe2e-app", "tail": 20},
    )
    ok(logs, "logs_tail")
    if TARGET == "incus":
        # The incus backend has no per-container stdout/stderr stream to serve:
        # its Logs read the instance CONSOLE (one raw PTY on PID 1), which for an
        # OCI application container carries the shell prompt, not the workload's
        # startup output — so the marker deterministically never appears here
        # (verified: logs_tail returns exactly "/ # "). The same limitation is why
        # compose-logs.star is not in SCENARIOS_INCUS. Everything else this
        # scenario covers (the graph, exec, file reads) works on incus, so keep
        # running it there and assert only that the tool call itself succeeds.
        log("logs_tail content assertion skipped on incus (console logs carry no app stdout)")
    else:
        assert_contains(logs["value"]["logs"], "mcp-app-ready")

    executed = mcp_call(
        handle = handle,
        tool = "exec_run",
        arguments = {
            "name": "mcpe2e-app",
            "cmd": ["sh", "-c", "printf mcp-exec-ok; printf mcp-exec-err >&2; exit 7"],
        },
    )
    ok(executed, "exec_run")
    assert_eq(executed["value"]["stdout"], "mcp-exec-ok", "exec stdout")
    assert_eq(executed["value"]["stderr"], "mcp-exec-err", "exec stderr")
    assert_eq(executed["value"]["exitCode"], 7, "exec exit code")

    files = mcp_call(handle = handle, tool = "files_list")
    ok(files, "files_list")
    compose_path = ""
    for f in files["value"]["files"]:
        if f["kind"] == "compose":
            compose_path = f["path"]
    assert_true(compose_path != "", "files_list did not expose the Compose fixture")
    read = mcp_call(
        handle = handle,
        tool = "file_read",
        arguments = {"path": compose_path},
    )
    ok(read, "file_read")
    assert_contains(read["value"]["content"], "mcp-app-ready")

    mcp_close(handle = handle)
    compose_down(file = compose_file)
    log("✓ MCP stdio reflected a live project and carried logs, exec, and file reads")
