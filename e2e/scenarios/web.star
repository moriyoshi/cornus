# Web UI end to end: stand up `cornus web` against a real deployed compose
# project and assert its /.cornus/web/* backend-for-frontend reflects the live
# workloads, the depends_on dependency graph, and the mounts. Then exercise the
# detached-frontend reverse-proxy mode against an in-process frontend_stub.
#
# Backend-agnostic: public images + a named volume, no build engine. Opt-in
# (make e2e-web / not in the default SCENARIOS list).

compose_file = "e2e/scenarios/web-compose.yaml"
project = "webe2e"

serve()

# Deploy the project fire-and-forget (no mounts/ports => no background helper).
compose_up(file = compose_file, detach = True)
wait(name = "webe2e-cache", running = 1, timeout = "180s")
wait(name = "webe2e-web", running = 1, timeout = "180s")

# Start the web UI + its BFF against the same server, loading the compose project
# so the project/graph/mounts endpoints have a project to describe.
base = web(compose_file = compose_file, project = project)

# config: reports the cornus server endpoint the BFF talks to.
cfg = http_get(url = base + "/.cornus/web/config")
assert_eq(cfg["status"], 200, "config endpoint")
assert_contains(cfg["body"], "endpoint")

# workloads: both project services appear by resource name (the compose<->server
# join `cornus compose ps` also does).
wl = http_get(url = base + "/.cornus/web/workloads")
assert_eq(wl["status"], 200, "workloads endpoint")
assert_contains(wl["body"], "webe2e-web")
assert_contains(wl["body"], "webe2e-cache")

# graph: the depends_on edge web -> cache.
g = http_get(url = base + "/.cornus/web/projects/" + project + "/graph")
assert_eq(g["status"], 200, "graph endpoint")
assert_contains(g["body"], "\"from\":\"web\"")
assert_contains(g["body"], "\"to\":\"cache\"")

# mounts: cache's named volume shows up by its container target.
m = http_get(url = base + "/.cornus/web/mounts")
assert_eq(m["status"], 200, "mounts endpoint")
assert_contains(m["body"], "/data")

# MCP: the co-hosted MCP server (on by default) answers on the same origin at
# /.cornus/mcp through the same BFF. A single Streamable-HTTP `initialize` proves
# the endpoint is live and identifies as cornus; the response is an SSE stream.
mcp_init = http(
    method = "POST",
    url = base + "/.cornus/mcp",
    body = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}',
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"},
)
assert_eq(mcp_init["status"], 200, "MCP initialize")
assert_contains(mcp_init["body"], "serverInfo")
assert_contains(mcp_init["body"], "cornus")

# --no-mcp removes the surface entirely: a second web server without MCP has no
# /.cornus/mcp route, so the request falls through to the SPA/root handler and is
# never answered by the MCP server.
base_nomcp = web(compose_file = compose_file, project = project, mcp = False)
mcp_off = http(
    method = "POST",
    url = base_nomcp + "/.cornus/mcp",
    body = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}',
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"},
)
assert_eq(mcp_off["body"].find("serverInfo"), -1, "--no-mcp must not serve the MCP endpoint")

# SPA root. In the integrated stack (the containerized runner and any UI-embedded
# build) this serves the real single-page app, so assert its actual HTML — the
# root mount node and a hashed asset reference — not merely a 200. A binary built
# WITHOUT the UI (node absent at build time) instead serves a 503 "run make web"
# notice; the BFF is fully functional either way, so tolerate that for node-less
# local builds.
root = http_get(url = base + "/")
if root["status"] == 200:
    assert_contains(root["body"], "id=\"root\"", "SPA root should be the embedded app HTML")
    assert_contains(root["body"], "/assets/", "SPA should reference its built assets")
else:
    assert_eq(root["status"], 503, "SPA root should be 200 (embedded) or 503 (not built), got %d" % root["status"])

# Detached-frontend mode: a second `cornus web` whose non-BFF requests reverse-
# proxy to a stand-in dev server (frontend_stub), while the BFF is still served
# at the same origin — the loop a developer uses to run Vite separately.
fe = frontend_stub()
base2 = web(frontend = fe)
proxied = http_get(url = base2 + "/")
assert_contains(proxied["body"], "FRONTEND-STUB", "root should proxy to the detached frontend")
cfg2 = http_get(url = base2 + "/.cornus/web/config")
assert_eq(cfg2["status"], 200, "BFF still served in detached-frontend mode")

log("✓ web UI BFF reflected the live project; detached-frontend proxy worked")

compose_down(file = compose_file)
