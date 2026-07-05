# What deleting an enrolled SSH key does to a session that was ALREADY minted.
#
# auth-ssh-key.star ends by proving a deleted key cannot mint a NEW session
# (`cornus auth token` is refused). That leaves the more operationally
# interesting half untested: an operator who deletes a key is usually trying to
# cut off access NOW, and the client holds a cached session that the deletion
# did not touch. This scenario pins the real contract in both directions —
# revocation stops MINTING, and existing sessions run to their TTL — so the
# bounded exposure is a stated property rather than a surprise.
#
# The cache wipe is what makes the warm-cache assertion mean anything. Without
# it, "the command still worked" could equally be a server that never enforced
# the deletion at all; the wipe forces the same command down the minting path,
# where it must now be refused.
#
# XDG_RUNTIME_DIR is redirected at a temp dir to isolate the token cache.
# CORNUS_AGENT_DIR would be the obvious knob and does NOT work here: tokencache
# runtimeDir() checks XDG_RUNTIME_DIR first, so on any host that sets it (every
# normal login session) the agent-dir override never applies.

data = temp_dir()
addr = serve(env = {
    "CORNUS_DATA": data,
    "CORNUS_AUTH_KEYSTORE": "file",
})
base = "http://" + addr

code = read_file(path = data + "/auth/enrollment.secret").strip()
work = temp_dir()
runtime = temp_dir()
key = work + "/id_ed25519"
generated = sh(cmd = "ssh-keygen -q -t ed25519 -N '' -f " + key)
assert_eq(generated["code"], 0, "ssh-keygen failed: " + generated["output"])

config = work + "/config.yaml"
client_env = {
    "CORNUS_CONFIG": config,
    "XDG_RUNTIME_DIR": runtime,
    "CORNUS_TOKEN_CACHE": "file",
}
cornus(
    "config", "set-context", "ssh-cache",
    "--server", base,
    "--key-auth-identity-file", key,
    "--key-auth-name", "e2e-cache-key",
    env = client_env,
)
cornus("config", "use-context", "ssh-cache", env = client_env)
cornus(
    "auth", "enroll",
    "--identity-file", key,
    "--name", "e2e-cache-key",
    "--code", code,
    env = client_env,
)

# A protected command mints a session and caches it. Asserting the token file
# exists is load-bearing twice over: it proves the redirected XDG_RUNTIME_DIR
# actually captured the cache (so the wipe below wipes the thing under test, not
# an empty directory), and it proves there IS a warm session, without which the
# next step would be vacuous.
tokens = runtime + "/cornus/tokens"
keys = cornus("auth", "keys", env = client_env)
assert_contains(keys, "e2e-cache-key", "the enrolled key was not listed through its SSH-key session")
cached = sh(cmd = "ls " + tokens + " 2>/dev/null | wc -l")
assert_eq(cached["output"].strip(), "1", "expected exactly one cached session under " + tokens)

fingerprint_result = sh(cmd = "ssh-keygen -lf " + key + ".pub | awk '{print $2}'")
assert_eq(fingerprint_result["code"], 0, "could not read the key fingerprint")
fingerprint = fingerprint_result["output"].strip()
cornus("auth", "delete-key", fingerprint, env = client_env)

# The key is gone, but the cached session is a bearer credential the server
# already issued: it stays valid for its remaining TTL. This is the CURRENT and
# intended behaviour (revocation gates minting, and sessions are short-lived).
# It is asserted rather than assumed so that making deletion revoke immediately
# becomes a deliberate change to this line, not a silent one.
warm = cornus("auth", "keys", env = client_env)
assert_contains(warm, "FINGERPRINT", "a cached session stopped working the moment its key was deleted")
assert_true(
    "e2e-cache-key" not in warm,
    "the deleted key is still listed: the deletion did not take effect server-side",
)

# Wipe the cache. The same command must now go down the minting path and be
# refused, which is what proves the step above was the CACHE and not an
# unenforced deletion.
#
# remove_all() rather than sh(cmd = "rm -rf ..."): the builtin only deletes
# inside a directory this scenario's own temp_dir() created, so a typo in this
# path is a refusal instead of a deletion somewhere real.
remove_all(path = tokens)
gone = sh(cmd = "ls " + tokens + " 2>/dev/null | wc -l")
assert_eq(gone["output"].strip(), "0", "the session cache was not actually wiped")

cold = cornus("auth", "keys", env = client_env, expect_fail = True)
assert_contains(cold, "not authorized", "a deleted SSH key minted a fresh session from a cold cache")

log("✓ deleting an SSH key stops new sessions being minted; an already-cached session runs to its TTL")
