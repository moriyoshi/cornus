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

# Shell discovery. This is the only check that runs the probe script through a REAL
# shell in a REAL image; every other test of it drives a fake exec, which can only
# prove the BFF asked the right question, not that a busybox `sh` answers it.
#
# The candidate list posted below is the browser's default. Both fixture images are
# alpine, so the answer discriminates: ash, sh and busybox exist; bash, zsh, dash
# and /busybox/sh do not. Asserting BOTH directions is the point — a handler that
# simply echoed its input back would satisfy every "is present" assertion on its own.
SHELL_CANDIDATES = '{"candidates":["/bin/zsh","/usr/bin/zsh","/bin/bash","/usr/bin/bash","/bin/dash","/usr/bin/dash","/bin/ash","/usr/bin/ash","/bin/sh","/usr/bin/sh","/busybox/sh","/bin/busybox sh","/usr/bin/busybox sh"]}'

shells = http(
    method = "POST",
    url = base + "/.cornus/web/workloads/webe2e-web/shells",
    body = SHELL_CANDIDATES,
    headers = {"Content-Type": "application/json"},
)
assert_eq(shells["status"], 200, "shell discovery")

# Scope every assertion to the `found` array. The response also echoes the resolved
# `candidates` it probed, which contains bash and zsh by construction — searching the
# whole body would report those as present and the negative assertions would be
# meaningless.
found = shells["body"][shells["body"].find('"found":'):]
assert_contains(found, '[["/bin/ash"]', "alpine's /bin/ash is present, and outranks /bin/sh")
assert_contains(found, '["/bin/sh"]', "alpine's /bin/sh is present")
assert_contains(found, '["/bin/busybox","sh"]', "a multi-word candidate is probed on its argv[0]")
assert_eq(found.find('["/bin/bash"]'), -1, "alpine has no bash")
assert_eq(found.find('["/bin/zsh"]'), -1, "alpine has no zsh")
assert_eq(found.find('["/bin/dash"]'), -1, "alpine has no dash")
assert_eq(found.find('["/busybox/sh"]'), -1, "alpine's busybox is /bin/busybox, not /busybox/sh")

# The service's own x-cornus-shells outranks the caller's list. `cache` declares
# /bin/sh, so it answers /bin/sh FIRST from the same request that answered /bin/ash
# for `web` — two different answers from one candidate list is what shows the
# service's declaration is being honoured rather than ignored.
cache_shells = http(
    method = "POST",
    url = base + "/.cornus/web/workloads/webe2e-cache/shells",
    body = SHELL_CANDIDATES,
    headers = {"Content-Type": "application/json"},
)
assert_eq(cache_shells["status"], 200, "shell discovery (cache)")
cache_found = cache_shells["body"][cache_shells["body"].find('"found":'):]
assert_contains(cache_found, '[["/bin/sh"]', "x-cornus-shells must rank ahead of the caller's list")

# A name with no container behind it is REFUSED rather than answered with an empty
# list: "this image has no shell" and "there is nothing here to ask" are different
# facts, and the UI words them differently (a command prompt versus an error).
#
# The code is 409, not 404, and that is measured rather than assumed. dockerhost's
# Status lists containers by label, so an unknown name yields an empty status with
# no error — indistinguishable from a deployment whose instances are all stopped —
# and ensureRunning reports "not running". Only a backend that errors outright
# reaches the 404 branch.
missing = http(
    method = "POST",
    url = base + "/.cornus/web/workloads/webe2e-nosuch/shells",
    body = SHELL_CANDIDATES,
    headers = {"Content-Type": "application/json"},
)
assert_eq(missing["status"], 409, "shell discovery on a name with no container")
assert_eq(missing["body"].find('"found"'), -1, "a refusal must not look like an empty answer")

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

# --- `cornus web --publish-in-conduit` -------------------------------------
#
# The third serving mode: no local port at all. The UI is hosted INSIDE the
# background client agent and published in the SHARED SOCKS5 conduit, so one
# browser proxy setting reaches both the workloads and the UI. Everything above
# dialled 127.0.0.1 directly; here the ONLY way in is through the proxy, by name.
#
# Two UIs are published on the SAME shared proxy on purpose. The headline
# assertion is that a published name is WITHDRAWN when its client exits (the
# agent parks on the held connection and reaps the registration when it closes,
# so the kernel is the liveness authority) — and with a single UI, "the name
# stopped resolving" would be indistinguishable from "the proxy went away with
# its last tenant". The survivor keeps the proxy up and makes the difference
# observable.
proxy = "127.0.0.1:" + free_port()
conduit = "socks5://" + proxy

name = web(publish = True, conduit = conduit, compose_file = compose_file, project = project)
assert_eq(name, "cornus.internal", "the UI should publish under the conduit suffix apex by default, got %r" % name)
name2 = web(publish = True, conduit = conduit, publish_name = "ui2.cornus.internal")

# The BFF is served through the conduit, and it is the same BFF: it still joins
# the live server workloads, reached over a proxy that resolves the name itself.
cfg3 = http_get(url = "http://" + name + "/.cornus/web/config", socks5 = proxy, retry = "30s")
assert_eq(cfg3["status"], 200, "the published UI must answer through the shared conduit")
assert_contains(cfg3["body"], "endpoint")
wl3 = http_get(url = "http://" + name + "/.cornus/web/workloads", socks5 = proxy, retry = "15s")
assert_eq(wl3["status"], 200, "workloads endpoint through the conduit")
assert_contains(wl3["body"], "webe2e-web", "the published BFF must reflect the same live workloads")
log("✓ the UI published in the conduit answers by name through the SOCKS5 proxy")

# Ctrl-C the first client. A clean exit is part of the contract — a UI you cannot
# stop without an error would be unusable — and the exit IS the withdrawal.
st = web_stop(handle = name)
assert_eq(st["code"], 0, "Ctrl-C on a published `cornus web` must exit cleanly, got %d" % st["code"])

gone = http_get(url = "http://" + name + "/.cornus/web/config", socks5 = proxy, retry = "5s", allow_error = True)
assert_true(gone.get("error", "") != "", "the published name must be WITHDRAWN once its client exits, got %r" % gone)

# ...and the proxy itself is still there, still serving the other UI — so the
# withdrawal above was the NAME going away, not the conduit.
still = http_get(url = "http://" + name2 + "/.cornus/web/config", socks5 = proxy, retry = "15s")
assert_eq(still["status"], 200, "the shared conduit must survive one tenant leaving, got %d" % still["status"])
log("✓ the published name was withdrawn on exit while the shared conduit kept serving the other UI")

web_stop(handle = name2)

compose_down(file = compose_file)
