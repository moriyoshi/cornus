# cornus token

Mint signed JWTs for a cornus server that uses bearer auth.

## Synopsis

```sh
cornus token issue [flags]
```

## Description

A cornus server is verify-only; `cornus token issue` is the in-process issuer
that mints the tokens it verifies. Sign with the same material the server
verifies against: an HS256 secret (`CORNUS_JWT_HS256_SECRET` on both sides) or a
private key whose public half is the server's `CORNUS_JWT_PUBLIC_KEY`. The
minted token is printed to stdout.

See [Security and authentication](/guides/security) for how the server verifies
tokens and how the claims map to access.

## cornus token issue

Mint one signed JWT.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--sub` | — | — | Subject (`sub`) claim — the caller identity. |
| `--scope` | — | `api` | Scope: `api` (full, the default meaning of an empty scope) or `caretaker` (the caretaker endpoint only). |
| `--ttl` | — | `1h` | Token lifetime (e.g. `1h`, `720h`). |
| `--iss` | — | — | Issuer (`iss`) claim; must match the server `CORNUS_JWT_ISSUER` when that is set. |
| `--aud` | — | — | Audience (`aud`) claim; must match the server `CORNUS_JWT_AUDIENCE` when that is set. |
| `--kid` | — | — | Key ID header, so a JWKS verifier (`CORNUS_JWT_JWKS_FILE`/`_URL`) can select the matching key. |
| `--hs256-secret` | `CORNUS_JWT_HS256_SECRET` | — | HMAC secret (symmetric); the server verifies with the same secret. At least 32 bytes. |
| `--private-key` | — | — | PEM private key file (RS256 for RSA, ES256 for ECDSA); the server verifies with the matching public key. |

## Examples

Mint an HS256-signed token with a symmetric secret:

```sh
export CORNUS_JWT_HS256_SECRET='a-secret-at-least-32-bytes-long!!'
cornus token issue --sub alice --ttl 24h
```

Mint an asymmetric token signed with a private key:

```sh
cornus token issue --sub ci --scope api --private-key ./signing-key.pem --kid key-1
```

Mint a caretaker-scoped token:

```sh
cornus token issue --sub sidecar --scope caretaker --aud cornus
```
