# cornus token

为使用 bearer auth 的 cornus 服务器签发已签名 JWT。

## 概要

```sh
cornus token issue [flags]
```

## 说明

cornus 服务器仅负责验证；`cornus token issue` 是进程内签发器，用于签发它所验证的 token。使用与服务器验证端相同的材料签名：HS256 secret（两端的 `CORNUS_JWT_HS256_SECRET`），或其公钥为服务器 `CORNUS_JWT_PUBLIC_KEY` 的 private key。签发的 token 会打印到 stdout。

服务器如何验证 token 以及 claim 如何映射为访问权限，请参见[安全与认证](/zh/guides/security)。

## cornus token issue

签发一个已签名 JWT。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--sub` | — | — | Subject（`sub`）claim，即调用方身份。 |
| `--scope` | — | `api` | Scope：`api`（完整权限，也是空 scope 的默认含义）或 `caretaker`（仅 caretaker 端点）。 |
| `--ttl` | — | `1h` | Token 有效期（例如 `1h`、`720h`）。 |
| `--iss` | — | — | Issuer（`iss`）claim；设置服务器 `CORNUS_JWT_ISSUER` 时必须与之匹配。 |
| `--aud` | — | — | Audience（`aud`）claim；设置服务器 `CORNUS_JWT_AUDIENCE` 时必须与之匹配。 |
| `--kid` | — | — | Key ID header，使 JWKS 验证器（`CORNUS_JWT_JWKS_FILE`/`_URL`）可选择匹配密钥。 |
| `--hs256-secret` | `CORNUS_JWT_HS256_SECRET` | — | HMAC secret（对称）；服务器使用同一 secret 验证。至少 32 字节。 |
| `--private-key` | — | — | PEM private key 文件（RSA 使用 RS256，ECDSA 使用 ES256）；服务器用匹配的 public key 验证。 |

## 示例

使用对称 secret 签发 HS256 token：

```sh
export CORNUS_JWT_HS256_SECRET='a-secret-at-least-32-bytes-long!!'
cornus token issue --sub alice --ttl 24h
```

使用 private key 签发非对称 token：

```sh
cornus token issue --sub ci --scope api --private-key ./signing-key.pem --kid key-1
```

签发 caretaker scope 的 token：

```sh
cornus token issue --sub sidecar --scope caretaker --aud cornus
```
