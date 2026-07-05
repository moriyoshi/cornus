# POST /.cornus/v1/build tar-upload endpoint. This is a thinner build surface than
# `cornus build` (which streams the context over 9P via build-attach): the whole
# build context is tar'd and POSTed in one request body with the target ref as ?t=,
# and the server streams text/plain build progress back, ending in a
# "BUILD OK <ref> <digest>" line. This scenario proves the endpoint (a) builds and
# reports BUILD OK, (b) delivered the uploaded context (the RUN marker streams back),
# and (c) pushed the resulting image into cornus's own registry.
# Needs the build engine (root / rootless); --target local is enough.

addr = serve()
ref = addr + "/e2e-upload/app:v1"

# no_cache so the RUN always executes and streams the marker (proves the tar upload
# actually delivered hello.txt into the build context).
res = build_upload(target = ref, context = "e2e/scenarios/build-upload", no_cache = True)
assert_eq(res["status"], 200, "POST /.cornus/v1/build should stream 200 (got %r)" % res["status"])
assert_contains(res["log"], "BUILD OK", "endpoint did not report BUILD OK: %r" % res["log"])
assert_contains(res["log"], "sha256:", "the BUILD OK line should carry the image digest")
assert_contains(res["log"], "uploaded-via-api-build", "uploaded context file was not present in the build")
log("✓ POST /.cornus/v1/build built from the uploaded tar and reported BUILD OK")

# The build pushed to cornus's registry (push defaults to true): the repo shows
# up in the catalog and its tag in the tag list.
cat = http_get(url = "http://" + addr + "/v2/_catalog")
assert_contains(cat["body"], "e2e-upload/app", "pushed repo missing from the registry catalog")
tags = http_get(url = "http://" + addr + "/v2/e2e-upload/app/tags/list")
assert_contains(tags["body"], "v1", "pushed tag missing from the tag list")
log("✓ tar-upload build pushed the image into cornus's registry")
