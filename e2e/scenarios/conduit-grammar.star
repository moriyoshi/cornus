# The --conduit / --conduit-mode selector grammar, driven through the real binary.
#
# A session-local proxy (any socks5:// URL with an authority) is a PER-RUN choice,
# so `set-context` rejects it; the socks5://.shared[:port] sentinel configures the
# context's SHARED proxy instead. Pure client-side config manipulation, so this is
# target-agnostic and needs no serve() / backend (like config-from-file.star). Every
# invocation points CORNUS_CONFIG at a throwaway file.
#
# Source of truth: cmd/cornus/internal/clientconn (ParseConduitSpec) and
# cmd/cornus/config.go (ConfigSetContextCmd). Mirrors the Go unit tests.

work = temp_dir()
cfg = work + "/config.yaml"
env = {"CORNUS_CONFIG": cfg}

# A session-local URL (an authority that is not the .shared sentinel) is a per-run
# choice and must be rejected by set-context, whether it pins a port or is ephemeral.
for url in ["socks5://127.0.0.1:1099", "socks5://"]:
    e = cornus("config", "set-context", "c", "--conduit-mode", url, env = env, expect_fail = True)
    assert_contains(e, "session-local", "%r must be rejected as a per-run choice (got %r)" % (url, e))
log("✓ set-context rejects a session-local socks5:// URL")

# The .shared sentinel with a port + suffix configures the context's SHARED proxy.
cornus("config", "set-context", "shared",
       "--conduit-mode", "socks5://.shared:1085?suffix=.demo.internal", env = env)
v = cornus("config", "view", "--context", "shared", env = env)
assert_contains(v, "listen: 127.0.0.1:1085", "the .shared sentinel should store the shared listen (got %r)" % v)
assert_contains(v, "service-host-suffix: .demo.internal", "the .shared sentinel should store the suffix")
log("✓ socks5://.shared:PORT configures the context's shared proxy")

# A bare word stores just the mode (join the shared proxy); no listen is pinned. Use
# a fresh config so the only context is this one (no other context's listen leaks in).
bareCfg = work + "/bare.yaml"
bareEnv = {"CORNUS_CONFIG": bareCfg}
cornus("config", "set-context", "bare", "--conduit-mode", "socks5", env = bareEnv)
v = cornus("config", "view", "--context", "bare", env = bareEnv)
assert_contains(v, "mode: socks5", "a bare word should store the socks5 mode (got %r)" % v)
assert_true("listen:" not in v, "a bare word must not pin a listen address (got %r)" % v)
log("✓ a bare socks5 word joins the shared proxy without pinning an address")
