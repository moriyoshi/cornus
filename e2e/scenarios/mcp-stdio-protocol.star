# MCP stdio protocol and process lifecycle. This scenario launches the real
# `cornus web --mcp-stdio` child through the official SDK client, keeps one
# session alive across discovery and tool calls, then closes stdin and requires
# the child to exit cleanly.

serve()

started = mcp_stdio()
handle = started["handle"]
assert_eq(started["server_name"], "cornus", "MCP initialize server identity")
assert_true(len(started["protocol_version"]) > 0, "MCP initialize negotiated no protocol version")

tools = mcp_list_tools(handle = handle)
for want in [
    "workloads_list",
    "workload_get",
    "project_graph",
    "file_read",
    "logs_tail",
    "exec_run",
    "activity_read",
]:
    assert_true(want in tools, "tools/list missing %s" % want)

resources = mcp_list_resources(handle = handle)
activity_uri = "cornus://activity/unfinished"
assert_true(activity_uri in resources, "resources/list missing the unfinished-activity resource")
activity = mcp_read_resource(handle = handle, uri = activity_uri)
assert_eq(activity["is_error"], False, "activity resource read")
assert_true(activity["value"] != None, "activity resource did not return JSON")

# A tool-domain error must be reported in-band without killing the session.
missing = mcp_call(
    handle = handle,
    tool = "workload_action",
    arguments = {"name": "mcp-e2e-does-not-exist", "action": "not-an-action"},
)
assert_eq(missing["is_error"], True, "invalid workload action should be an MCP tool error")

again = mcp_call(handle = handle, tool = "workloads_list")
assert_eq(again["is_error"], False, "session did not survive a tool error")

# ClientSession.Close closes the child's stdin and waits for its exit. The
# builtin fails if the process needs forced termination or exits non-zero.
mcp_close(handle = handle)

conflict = cornus("web", "--mcp-stdio", "--publish-in-conduit", expect_fail = True)
assert_contains(conflict, "mutually exclusive", "stdio/published mode conflict")

log("✓ MCP stdio initialized, served repeated requests, and shut down cleanly")
