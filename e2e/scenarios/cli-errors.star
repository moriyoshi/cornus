# CLI input-validation and fail-closed-startup paths. The happy CLI scenario
# (cli.star) proves version / health / push / deploy work; this proves the CLI
# rejects bad input and the server refuses to boot on a malformed config, all via
# cornus(..., expect_fail=True), which returns the combined output so we can
# assert_contains on the exact diagnostic. Target-agnostic: pure CLI/startup
# validation, no deploy backend needed (no serve() required).
#
# Source of truth: cmd/cornus/portforward.go, cmd/cornus/commands.go,
# cmd/cornus/config.go, and pkg/server/gcschedule.go / server.go.

# --- 1. port-forward: invalid port mapping ------------------------------------
# parsePortSpec runs before any dial, so a bad mapping fails fast with no server.
pf = cornus("port-forward", "somedep", "abc:def", expect_fail = True)
assert_contains(pf, "invalid port mapping", "a non-numeric port mapping should be rejected (got %r)" % pf)
log("✓ port-forward rejects an invalid port mapping")

# --- 2. deploy -f: spec validation --------------------------------------------
# spec.name is checked before the connection is resolved, so this needs no server.
work = temp_dir()
noname = work + "/noname.yaml"
write_file(path = noname, content = "image: alpine:3.20\n")
d1 = cornus("deploy", "-f", noname, expect_fail = True)
assert_contains(d1, "spec.name is required", "a spec with no name should be rejected (got %r)" % d1)

badyaml = work + "/bad.yaml"
write_file(path = badyaml, content = "name: x\n  image: : : broken\n\t- nope\n")
d2 = cornus("deploy", "-f", badyaml, expect_fail = True)
assert_contains(d2, "parsing spec", "a malformed spec YAML should be rejected with a parse error (got %r)" % d2)
log("✓ deploy -f rejects a nameless spec and malformed YAML")

# --- 3. config: unknown context ------------------------------------------------
# Point CORNUS_CONFIG at a throwaway file so the user's real config is untouched;
# an empty config has no contexts, so use-context of any name is "not found".
cfg = work + "/client.yaml"
cenv = {"CORNUS_CONFIG": cfg}
c1 = cornus("config", "use-context", "ghost", env = cenv, expect_fail = True)
assert_contains(c1, 'context "ghost" not found', "use-context of an unknown context should fail (got %r)" % c1)
log("✓ config use-context rejects an unknown context")

# --- 4. fail-closed startup: malformed server config ---------------------------
# The server validates env at startup (in server.New) BEFORE binding the listener,
# so a malformed value makes `cornus serve` exit non-zero immediately.
listen = free_port()
gc = cornus("serve", "--addr", listen, "--storage", "mem://",
            env = {"CORNUS_GC_INTERVAL": "nonsense"}, expect_fail = True)
assert_contains(gc, "invalid CORNUS_GC_INTERVAL", "a malformed CORNUS_GC_INTERVAL should fail startup (got %r)" % gc)

listen2 = free_port()
pol = cornus("serve", "--addr", listen2, "--storage", "mem://",
             env = {"CORNUS_API_POLICY": "{not valid json"}, expect_fail = True)
assert_contains(pol, "invalid CORNUS_API_POLICY", "a malformed CORNUS_API_POLICY should fail startup (got %r)" % pol)
log("✓ the server refuses to boot on a malformed CORNUS_GC_INTERVAL / CORNUS_API_POLICY (fail-closed)")
