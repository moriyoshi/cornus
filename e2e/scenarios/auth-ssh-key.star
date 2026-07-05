# SSH-public-key client authentication: enroll with the local server data-dir
# code, use the saved key-auth profile to mint a short-lived API session, rotate
# the enrollment code, administer keys, and reject the deleted key.

data = temp_dir()
addr = serve(env = {
    "CORNUS_DATA": data,
    "CORNUS_AUTH_KEYSTORE": "file",
})
base = "http://" + addr

anon = http(method = "GET", url = base + "/.cornus/v1/deploy")
assert_eq(anon["status"], 401, "enabling the SSH key store must enable client authentication")

code = read_file(path = data + "/auth/enrollment.secret").strip()
assert_true(len(code) > 20, "the server did not create an enrollment code")

work = temp_dir()
key = work + "/id_ed25519"
generated = sh(cmd = "ssh-keygen -q -t ed25519 -N '' -f " + key)
assert_eq(generated["code"], 0, "ssh-keygen failed: " + generated["output"])

config = work + "/config.yaml"
client_env = {"CORNUS_CONFIG": config}
cornus(
    "config", "set-context", "ssh-auth",
    "--server", base,
    "--key-auth-identity-file", key,
    "--key-auth-name", "e2e-key",
    env = client_env,
)
cornus("config", "use-context", "ssh-auth", env = client_env)
cornus(
    "auth", "enroll",
    "--identity-file", key,
    "--name", "e2e-key",
    "--code", code,
    env = client_env,
)

# Successful enrollment rotates the code. A second key cannot reuse it.
key2 = work + "/id_ed25519_second"
generated2 = sh(cmd = "ssh-keygen -q -t ed25519 -N '' -f " + key2)
assert_eq(generated2["code"], 0, "second ssh-keygen failed: " + generated2["output"])
reused = cornus(
    "auth", "enroll",
    "--identity-file", key2,
    "--name", "replay",
    "--code", code,
    env = client_env,
    expect_fail = True,
)
assert_contains(reused, "enrollment code", "the consumed enrollment code was accepted again")

# Ordinary protected commands resolve key-auth, mint a session, and use it.
keys = cornus("auth", "keys", env = client_env)
assert_contains(keys, "e2e-key", "the enrolled key was not listed through its SSH-key session")

fingerprint_result = sh(cmd = "ssh-keygen -lf " + key + ".pub | awk '{print $2}'")
assert_eq(fingerprint_result["code"], 0, "could not read the key fingerprint")
fingerprint = fingerprint_result["output"].strip()
cornus("auth", "delete-key", fingerprint, env = client_env)

deleted = cornus(
    "auth", "token",
    "--identity-file", key,
    env = client_env,
    expect_fail = True,
)
assert_contains(deleted, "not authorized", "a deleted SSH key minted a new session")
log("✓ SSH key enrollment, session minting, code rotation, listing, and deletion")
