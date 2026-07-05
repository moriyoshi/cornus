# cornus auth

Enroll and manage SSH public keys that mint short-lived Cornus client sessions.
The private key stays on the client; the server stores only its public key.

## Synopsis

```sh
cornus auth enrollment-code
cornus auth enroll [flags]
cornus auth token [flags]
cornus auth keys [flags]
cornus auth delete-key <SHA256-fingerprint> [flags]
```

## Enrollment

Enable key authentication on the server with `CORNUS_AUTH_KEYSTORE=file` or
`CORNUS_AUTHORIZED_KEYS`. The default server posture is unchanged: with neither
setting and no existing key store, these routes are not registered.

`cornus auth enrollment-code` is a local command. It reads
`<CORNUS_DATA>/auth/enrollment.secret` without calling the server, so run it as
the server user or through `ssh`, `docker exec`, or `kubectl exec`. The code is
rotated after each successful enrollment.

```sh
# On the server host:
code=$(cornus auth enrollment-code)

# On the client:
cornus auth enroll --server https://cornus.example \
  --identity-file ~/.ssh/id_ed25519 --name laptop --code "$code"
```

Use `--key-fingerprint SHA256:...` instead of `--identity-file` to select a key
from `SSH_AUTH_SOCK`. RSA proofs always use SHA-2; legacy RSA/SHA-1 signatures
are rejected.

## Connection profiles

A `key-auth` profile stores an identity-file path or ssh-agent fingerprint,
never private-key material or a session token:

```sh
cornus config set-context prod \
  --server https://cornus.example \
  --key-auth-identity-file ~/.ssh/id_ed25519 \
  --key-auth-name laptop
cornus config use-context prod
```

Ordinary commands then prove possession of the key and use a short-lived bearer
session automatically. Credential precedence is `CORNUS_TOKEN`, then `key-auth`,
then `kube-auth`, then a static profile `token`. A profile cannot contain both
`key-auth` and `kube-auth`.

Sessions default to the `api` scope and a one-hour lifetime (maximum 24 hours).
They are cached privately in the runtime directory and considered expired two
minutes early. Foreground commands mint and refresh the cache; the background
client agent only reads it.

## Token and key administration

`cornus auth token` explicitly mints a session and prints it. `cornus auth keys`
lists authorized public keys, and `cornus auth delete-key` removes a
runtime-enrolled key. Listing and deletion require a full credential.

```sh
cornus auth token --identity-file ~/.ssh/id_ed25519 --scope api --ttl 30m
cornus auth keys
cornus auth delete-key SHA256:abc123...
```

Keys supplied through `CORNUS_AUTHORIZED_KEYS` are declarative and cannot be
deleted through the API. Deleting a key prevents new sessions; already-issued
stateless JWTs remain valid until their short expiry.

**See also:** [Security and authentication](/guides/security),
[cornus config](/cli/config), [Connection config](/reference/connection-config).
