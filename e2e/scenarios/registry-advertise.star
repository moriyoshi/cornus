# registry-advertise.star — the server advertises a registry host at GET /.cornus/v1/info
# and redirects a build push aimed at that host into its own co-located registry.
#
# On a real cluster a deploy pull ref must carry an address a NODE can reach, which
# differs from the client's endpoint; the server reports it at /.cornus/v1/info and the
# compose client bakes it in. The build engine, running inside the pod, cannot reach
# that node address to PUSH, so the server rewrites a push at the advertised host to
# its own loopback registry, keeping the repository path. This scenario drives both
# behaviors through the real server with CORNUS_ADVERTISE_REGISTRY set — no deploy
# backend needed, so it is target-agnostic (build_upload posts straight to
# /.cornus/v1/build and never PrepareImages).

ADV = "advertised.example:5000"

addr = serve(env = {"CORNUS_ADVERTISE_REGISTRY": ADV})

# 1. /.cornus/v1/info reflects the configured advertised host and a scheme (http, no TLS).
info = http_get("http://" + addr + "/.cornus/v1/info")
assert_eq(info["status"], 200, "GET /.cornus/v1/info is 200")
assert_contains(info["body"], ADV, "/.cornus/v1/info reports the advertised registry_host")
assert_contains(info["body"], "\"registry_scheme\":\"http\"", "/.cornus/v1/info reports http scheme")

# 2. A push aimed at the (unreachable) advertised host is redirected server-side into
# the co-located registry. The build therefore SUCCEEDS even though nothing listens
# at advertised.example:5000 — without the redirect the push would fail — and the
# image is then servable under the same repository path on the real endpoint.
res = build_upload(target = ADV + "/advtest:v1", context = "e2e/scenarios/app")
assert_eq(res["status"], 200, "build_upload HTTP status")
assert_contains(res["log"], "BUILD OK", "build+push succeeded (push redirected to co-located registry)")

tags = http_get("http://" + addr + "/v2/advtest/tags/list")
assert_eq(tags["status"], 200, "tags/list is 200 (repo exists in the co-located registry)")
assert_contains(tags["body"], "v1", "advtest:v1 landed in the co-located registry despite the unreachable tag host")
