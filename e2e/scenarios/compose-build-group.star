# Regression test: cornus dedupes compose services that share an identical
# build: context/Dockerfile into ONE BuildKit build (groupBuildRequests) — the
# first member's tag becomes that build's primary Target, and every other
# member's tag rides along as an additional Tag on the same build. The server
# redirects a push aimed at its own advertised (cluster-node-only) registry
# host onto its co-located loopback registry, because the in-pod build engine
# cannot reach that host itself (see registry-advertise.star). A past bug
# applied that redirect only to the Target, so a build group's
# second-and-later member tags stayed pointed at the unreachable advertised
# host and failed to push. This drives the build-group path through the real
# server with CORNUS_ADVERTISE_REGISTRY set and asserts BOTH members' images
# land in the co-located registry. Target-agnostic: only compose_build runs,
# so no deploy backend is exercised.

ADV = "advertised.example:5000"

compose_file = "e2e/scenarios/compose-build-group.yaml"

addr = serve(env = {"CORNUS_ADVERTISE_REGISTRY": ADV})

compose_build(file = compose_file, project = "cbgrp")
log("✓ compose build produced the build-group's images")

for svc in ["svc-a", "svc-b"]:
    repo = "cbgrp-" + svc
    tags = http_get("http://" + addr + "/v2/" + repo + "/tags/list")
    assert_eq(tags["status"], 200, repo + " tags/list is 200 (landed in the co-located registry)")
    assert_contains(tags["body"], "latest", repo + ":latest present")

log("✓ every build-group member's tag was redirected and pushed, not just the first")
