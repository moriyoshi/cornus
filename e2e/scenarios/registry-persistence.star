# Registry persistence across a server restart, on the filesystem storage
# backend. Push an image, stop the server, start a NEW server against the SAME
# --storage dir, and confirm the image's manifest + tag survived. The default
# mem:// backend would lose it on restart — this proves file:// persistence.
# Target-agnostic (registry only; no deploy backend needed).

# A dedicated on-disk storage dir the two server incarnations share.
store = "file://" + temp_dir()

serve(storage = store)
digest = registry_roundtrip(ref = "persist/app:v1")
assert_contains(digest, "sha256:")
log("pushed " + digest + " to the file-backed registry")

stop_server()

# A fresh server process over the SAME storage dir.
addr = serve(storage = store)
log("restarted against the same storage dir")

# The tag list still knows the repo...
tags = http_get(url = "http://" + addr + "/v2/persist/app/tags/list")
assert_eq(tags["status"], 200, "tags/list unreachable after restart")
assert_contains(tags["body"], "v1")

# ...and the manifest (hence its blobs) is still served.
man = http_get(url = "http://" + addr + "/v2/persist/app/manifests/v1")
assert_eq(man["status"], 200, "manifest gone after restart — persistence broken")
log("✓ image survived the restart (file:// persistence)")
