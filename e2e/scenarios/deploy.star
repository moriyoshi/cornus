# Full path: build an image into cornus's registry, deploy it via the target
# backend (dockerhost or kubernetes), wait for replicas to become ready, then
# tear down. On the kube target, build() also loads the image into kind.

serve()

image = build(
    name = "demo",
    context = "e2e/scenarios/app",
    args = {"GREETING": "from-e2e"},
)
log("built image: " + image)

deploy(name = "demo", image = image, replicas = 2)

st = wait(name = "demo", running = 2, timeout = "180s")
assert_eq(st["running"], 2, "expected 2 running instances")
assert_eq(st["total"], 2)

# Idempotent re-apply must keep it at 2.
deploy(name = "demo", image = image, replicas = 2)
st = wait(name = "demo", running = 2, timeout = "180s")
assert_eq(st["running"], 2)

remove(name = "demo")
log("torn down")
