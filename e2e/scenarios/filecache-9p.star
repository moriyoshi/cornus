# Server-side per-file block cache over the remote lazy-build 9P path.
#
# A remote (--builder) lazy build serves the caller's named context on demand: the
# server kernel-9p-mounts the caller's export and pulls only the touched bytes,
# which the caller reports as "CORNUS-9P-BACKING served N bytes". With the server
# block cache enabled (CORNUS_FILE_CACHE=1), a chunk pulled once is served from
# the server's on-disk cache on subsequent reads. So building the SAME immutable
# context twice must pull fewer bytes from the caller the second time — ideally
# zero, since the touched file's chunk is already cached.
#
# Like build-lazy-9p.star this drives the remote lazy path, which kernel-mounts a
# 9p backing on the server, so it needs the 9p KERNEL MODULE (the lazy_9p path's
# Cap9P). It is deliberately NOT in the default Makefile suite; run it explicitly
# in a privileged, 9p-capable environment, e.g.
#   make e2e-one TARGET=local SCENARIO=e2e/scenarios/filecache-9p.star
# Requires the build engine (root / rootless); --target local is enough.

MiB = 1048576

# served_bytes extracts N from a "<marker>N bytes" line in the log.
def served_bytes(log, marker):
    i = log.find(marker)
    if i < 0:
        fail(msg = "no '%s' marker in the build log (lazy 9p backing not taken)" % marker)
    rest = log[i + len(marker):]
    j = rest.find(" ")
    return int(rest[:j])

# Boot a server with the per-file block cache enabled. The cache directory is
# mandatory when the cache is on (no default); a relative value roots under the
# server's data dir (CORNUS_DATA), which is all a test needs.
serve(env = {"CORNUS_FILE_CACHE": "1", "CORNUS_FILE_CACHE_DIR": "filecache"})

# A named-context dir with a big untouched file + the small file the build reads.
# It is not modified between builds, so its cache identity (size+mtime) is stable
# and the second build's read is a cache hit.
datadir = temp_dir()
r = sh(cmd = "truncate -s 16M " + datadir + "/big.bin")
assert_eq(r["code"], 0, "make the 16 MiB sparse file")
write_file(path = datadir + "/small.txt", content = "SMALL")

def remote_lazy_build():
    return build(
        name = "filecacheapp",
        context = "e2e/scenarios/build-lazy",
        build_context = {"data": datadir},
        builder = True,
        lazy = True,
        no_cache = True,
        no_push = True,
        capture = True,
    )

# First build: cold cache — the server pulls the touched bytes from the caller.
first = remote_lazy_build()
assert_contains(first["log"], "LAZY-COPY-OK")
n1 = served_bytes(first["log"], "CORNUS-9P-BACKING served ")
log("cold build: backing served %d bytes" % n1)
assert_true(n1 > 0, "cold build should pull the touched bytes from the caller, served %d" % n1)
assert_true(n1 < MiB, "cold build served %d bytes (>=1 MiB); lazy fetch not effective" % n1)

# Second build: warm cache — the same chunk is served from the server's on-disk
# block cache, so the caller serves fewer bytes than the first time.
second = remote_lazy_build()
assert_contains(second["log"], "LAZY-COPY-OK")
n2 = served_bytes(second["log"], "CORNUS-9P-BACKING served ")
log("warm build: backing served %d bytes (was %d)" % (n2, n1))
assert_true(n2 < n1, "warm build served %d bytes, expected fewer than the cold build's %d (block cache not effective)" % (n2, n1))
log("✓ server block cache cut the second build's 9P pull from %d to %d bytes" % (n1, n2))
