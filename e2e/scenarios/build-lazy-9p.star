# Lazy build over a KERNEL 9p mount, plus the remote --builder variant — the two
# lazy paths build-lazy.star does NOT exercise. build-lazy.star serves a lazy
# context over the default host-dir bind; here:
#
#   - The LOCAL variant sets the CORNUS_LAZY_9P sub-toggle (via build's lazy_9p),
#     which backs the lazy named context with a real kernel-9p mount of an
#     in-process p9 server. The engine prints "CORNUS-9P served N bytes".
#   - The REMOTE variant runs the same lazy build over --builder; the server
#     kernel-mounts the caller's 9p export of the context, and the caller prints
#     "CORNUS-9P-BACKING served N bytes".
#
# The build touches only small.txt from the 16 MiB "data" context; big.bin is
# never read, so served bytes must stay far below 1 MiB in both variants.
#
# Both paths need the 9p KERNEL MODULE (stronger than root alone), so this scenario
# is gated on Cap9P: preflight fails fast with a "load 9p/9pnet" hint when the
# kernel lacks 9p (the lazy_9p token drives the need). It is NOT in the default
# Makefile suite — run it explicitly in a privileged, 9p-capable environment, e.g.
#   cornus-e2e --target local e2e/scenarios/build-lazy-9p.star
# Committed form of the .agents-workspace/tmp/lazy-9p-measure and remote-lazy
# scratch harnesses. Requires the build engine (root / rootless); --target local
# is enough.

MiB = 1048576

# served_bytes extracts N from a "<marker>N bytes" line in the log.
def served_bytes(log, marker):
    i = log.find(marker)
    if i < 0:
        fail(msg = "no '%s' marker in the build log (lazy 9p path not taken)" % marker)
    rest = log[i + len(marker):]
    j = rest.find(" ")
    return int(rest[:j])

serve()

# A named-context dir with a big untouched file + the small file the build reads.
datadir = temp_dir()
r = sh(cmd = "truncate -s 16M " + datadir + "/big.bin")
assert_eq(r["code"], 0, "make the 16 MiB sparse file")
write_file(path = datadir + "/small.txt", content = "SMALL")

# --- local kernel-9p variant ------------------------------------------------
# lazy_9p=True sets CORNUS_LAZY_9P for the in-process engine, so the named
# context is served over a kernel-9p mount and served bytes are reported.
local = build(
    name = "lazy9papp",
    context = "e2e/scenarios/build-lazy",
    build_context = {"data": datadir},
    lazy_9p = True,
    no_cache = True,
    no_push = True,
    capture = True,
)
assert_contains(local["log"], "LAZY-COPY-OK")
n = served_bytes(local["log"], "CORNUS-9P served ")
log("local kernel-9p lazy build served %d bytes of a 16 MiB context" % n)
assert_true(n < MiB, "local lazy 9p transferred %d bytes (>=1 MiB); kernel-9p lazy fetch not effective" % n)
log("✓ local kernel-9p lazy build transferred only the touched bytes")

# --- remote --builder variant -----------------------------------------------
# The same lazy build run remotely: the server kernel-mounts the caller's 9p
# export of the context, so only touched bytes cross the wire; the caller reports
# them as "CORNUS-9P-BACKING served N bytes".
remote = build(
    name = "lazy9papp",
    context = "e2e/scenarios/build-lazy",
    build_context = {"data": datadir},
    builder = True,
    lazy = True,
    no_cache = True,
    no_push = True,
    capture = True,
)
assert_contains(remote["log"], "LAZY-COPY-OK")
m = served_bytes(remote["log"], "CORNUS-9P-BACKING served ")
log("remote lazy build backing served %d bytes of a 16 MiB context" % m)
assert_true(m < MiB, "remote lazy build backing served %d bytes (>=1 MiB); lazy 9p backing not effective" % m)
log("✓ remote lazy build pulled only the touched bytes over 9P")
