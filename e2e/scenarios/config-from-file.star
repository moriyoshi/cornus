# `cornus config set-context` file-backed input and merge-vs-replace semantics.
# Pure client-side config manipulation — set-context only reads/writes the client
# config file — so this is target-agnostic and needs no serve() / deploy backend
# (like cli-errors.star). Every invocation points CORNUS_CONFIG at a throwaway file
# under a temp dir, so the developer's real ~/.config/cornus/config.yaml is untouched.
#
# Source of truth: cmd/cornus/config.go (ConfigSetContextCmd) and
# cmd/cornus/contextfile.go (loadContextFile / mergeContext). Mirrors the Go unit
# tests in cmd/cornus/config_test.go, but drives the real built binary end to end.

work = temp_dir()

# --- 1. --from-file loads a whole bare-context object --------------------------
cfg = work + "/full.yaml"
env = {"CORNUS_CONFIG": cfg}
ctxfile = work + "/ctx.yaml"
write_file(path = ctxfile, content = "\n".join([
    "server: https://from-file",
    "token: filetoken",
    "tls:",
    "  ca-cert: /ca.pem",
    "port-forward:",
    "  namespace: cornus",
    "",
]))
cornus("config", "set-context", "prod", "--from-file", ctxfile, env = env)
v = cornus("config", "view", "--show-tokens", env = env)
assert_contains(v, "server: https://from-file", "--from-file should load the server (got %r)" % v)
assert_contains(v, "token: filetoken", "--from-file should load the token")
assert_contains(v, "ca-cert: /ca.pem", "--from-file should load the nested tls block")
assert_contains(v, "namespace: cornus", "--from-file should load the nested port-forward block")
log("✓ --from-file loaded a full context object")

# --- 2. precedence: individual --flags override --from-file (a base layer) ------
cfg2 = work + "/prec.yaml"
env2 = {"CORNUS_CONFIG": cfg2}
base = work + "/base.yaml"
write_file(path = base, content = "server: https://A\ntoken: T\n")
cornus("config", "set-context", "prod", "--from-file", base, "--server", "https://cli-wins", env = env2)
v = cornus("config", "view", "--show-tokens", env = env2)
assert_contains(v, "server: https://cli-wins", "an explicit --server must override --from-file (got %r)" % v)
assert_contains(v, "token: T", "a field the flags leave unset stays from --from-file")
log("✓ --from-file is a base layer the individual flags override")

# --- 3. precedence: --from-file-override wins over the individual --flags -------
cfg3 = work + "/over.yaml"
env3 = {"CORNUS_CONFIG": cfg3}
force = work + "/force.yaml"
write_file(path = force, content = "server: https://from-file\n")
cornus("config", "set-context", "prod", "--server", "https://cli-loses", "--from-file-override", force, env = env3)
v = cornus("config", "view", "--show-tokens", env = env3)
assert_contains(v, "server: https://from-file", "--from-file-override must win over --server (got %r)" % v)
assert_true("cli-loses" not in v, "the CLI --server must have been overridden")
log("✓ --from-file-override wins over the individual flags")

# --- 4. repeated --from-file merges left-to-right, later files winning ----------
cfg4 = work + "/repeat.yaml"
env4 = {"CORNUS_CONFIG": cfg4}
first = work + "/first.yaml"
second = work + "/second.yaml"
write_file(path = first, content = "server: https://A\ntoken: T1\n")
write_file(path = second, content = "server: https://A2\n")
cornus("config", "set-context", "prod", "--from-file", first, "--from-file", second, env = env4)
v = cornus("config", "view", "--show-tokens", env = env4)
assert_contains(v, "server: https://A2", "the later --from-file should override the earlier server (got %r)" % v)
assert_contains(v, "token: T1", "the earlier --from-file token survives (the later file omits it)")
log("✓ repeated --from-file merges left-to-right")

# --- 5. merge vs replace (the default) -----------------------------------------
cfg5 = work + "/edit.yaml"
env5 = {"CORNUS_CONFIG": cfg5}
# Seed a context with several fields, including nested blocks.
cornus("config", "set-context", "edit", "--server", "https://prod",
       "--token", "t1", "--tls-ca-cert", "/ca.pem", env = env5)
# --merge edits in place: only the token changes; server + tls are preserved.
cornus("config", "set-context", "edit", "--token", "t2", "--merge", env = env5)
v = cornus("config", "view", "--show-tokens", env = env5)
assert_contains(v, "server: https://prod", "--merge must preserve the unset server (got %r)" % v)
assert_contains(v, "ca-cert: /ca.pem", "--merge must preserve the unset tls block")
assert_contains(v, "token: t2", "--merge must apply the given token")
log("✓ --merge edits in place, preserving unset fields")
# The default replaces: everything not named on this invocation is dropped.
cornus("config", "set-context", "edit", "--token", "t3", env = env5)
v = cornus("config", "view", "--show-tokens", env = env5)
assert_contains(v, "token: t3", "replace must apply the given token (got %r)" % v)
assert_true("https://prod" not in v, "replace (the default) must drop the stored server")
assert_true("ca-cert" not in v, "replace (the default) must drop the stored tls block")
log("✓ the default replaces the context, dropping unnamed fields")

# --- 6. error paths ------------------------------------------------------------
# A full config document (top-level contexts:) is rejected by the strict decode.
fullcfg = work + "/wholeconfig.yaml"
write_file(path = fullcfg, content = "contexts:\n  prod:\n    server: https://x\n")
e1 = cornus("config", "set-context", "bad", "--from-file", fullcfg, env = env, expect_fail = True)
assert_contains(e1, 'unknown field "contexts"', "a full-config document must be rejected (got %r)" % e1)
# A missing file errors for either flag.
missing = work + "/nope.yaml"
e2 = cornus("config", "set-context", "bad", "--from-file", missing, env = env, expect_fail = True)
assert_contains(e2, "no such file", "a missing --from-file path must error (got %r)" % e2)
e3 = cornus("config", "set-context", "bad", "--from-file-override", missing, env = env, expect_fail = True)
assert_contains(e3, "no such file", "a missing --from-file-override path must error (got %r)" % e3)
# An invalid SOCKS5 resolve regexp in the file is rejected.
badre = work + "/badre.yaml"
write_file(path = badre, content = "\n".join([
    "conduit:",
    "  socks5:",
    "    resolve:",
    "    - pattern: \"([bad\"",
    "      replace: svc:80",
    "",
]))
e4 = cornus("config", "set-context", "bad", "--from-file", badre, env = env, expect_fail = True)
assert_contains(e4, "invalid resolve pattern", "an invalid resolve regexp must be rejected (got %r)" % e4)
log("✓ error paths: full-config / missing file / bad resolve regexp all rejected")

# --- 7. view --export dumps one bare context that round-trips via --from-file ---
cfg7 = work + "/export.yaml"
env7 = {"CORNUS_CONFIG": cfg7}
cornus("config", "set-context", "src", "--server", "https://exported",
       "--token", "sekret", "--tls-ca-cert", "/ca.pem", env = env7)
# Export to a file, then read it back: no contexts: wrapper, real token included.
dump = work + "/dump.yaml"
cornus("config", "view", "--context", "src", "--export", "--output-file", dump, env = env7)
exported = read_file(dump)
assert_true("contexts:" not in exported, "--export must omit the contexts: wrapper (got %r)" % exported)
assert_contains(exported, "server: https://exported", "--export should carry the server")
assert_contains(exported, "token: sekret", "--export includes the real token by default")
# Feed the dump straight back into a fresh context (the round-trip).
cornus("config", "set-context", "dst", "--from-file", dump, env = env7)
v = cornus("config", "view", "--show-tokens", env = env7)
assert_contains(v, "dst:", "the re-imported context should exist")
# --redact strips the token from the export.
red = cornus("config", "view", "--context", "src", "--export", "--redact", env = env7)
assert_contains(red, "token: REDACTED", "--redact should hide the token (got %r)" % red)
assert_true("sekret" not in red, "--redact must not leak the token")
log("✓ view --export round-trips through --from-file; --redact strips the token")
