# cornus auth

注册并管理 SSH 公钥，用于签发短期 Cornus 客户端会话。私钥保留在客户端，服务器只保存公钥。

## 用法

```sh
cornus auth enrollment-code
cornus auth enroll [flags]
cornus auth token [flags]
cornus auth keys [flags]
cornus auth delete-key <SHA256-fingerprint> [flags]
```

## 注册

在服务器上设置 `CORNUS_AUTH_KEYSTORE=file` 或 `CORNUS_AUTHORIZED_KEYS` 以启用密钥身份验证。服务器的默认状态不会改变: 若两者都未设置且不存在已有密钥存储，这些路由不会注册。

`cornus auth enrollment-code` 是本地命令。它不调用服务器，而是读取 `<CORNUS_DATA>/auth/enrollment.secret`，因此请以服务器用户身份运行，或通过 `ssh`、`docker exec`、`kubectl exec` 运行。每次成功注册后，代码都会轮换。

```sh
# 在服务器主机上:
code=$(cornus auth enrollment-code)

# 在客户端上:
cornus auth enroll --server https://cornus.example \
  --identity-file ~/.ssh/id_ed25519 --name laptop --code "$code"
```

使用 `--key-fingerprint SHA256:...` 代替 `--identity-file`，可从 `SSH_AUTH_SOCK` 选择密钥。RSA 证明始终使用 SHA-2，并拒绝旧式 RSA/SHA-1 签名。

## 连接配置文件

`key-auth` 配置文件只保存私钥文件路径或 ssh-agent 指纹，绝不保存私钥内容或会话令牌。

```sh
cornus config set-context prod \
  --server https://cornus.example \
  --key-auth-identity-file ~/.ssh/id_ed25519 \
  --key-auth-name laptop
cornus config use-context prod
```

普通命令会证明持有该密钥，并自动使用短期 bearer 会话。凭据优先级依次为 `CORNUS_TOKEN`、`key-auth`、`kube-auth`、静态配置文件 `token`。同一配置文件不能同时包含 `key-auth` 和 `kube-auth`。

会话默认使用 `api` scope，有效期为 1 小时 (最长 24 小时)。会话以私有方式缓存在运行时目录中，并提前 2 分钟视为过期。前台命令签发并刷新缓存，后台客户端 agent 只读取缓存。

## 令牌和密钥管理

`cornus auth token` 显式签发并打印会话。`cornus auth keys` 列出已授权的公钥，`cornus auth delete-key` 删除运行时注册的密钥。列出和删除操作需要完全访问凭据。

```sh
cornus auth token --identity-file ~/.ssh/id_ed25519 --scope api --ttl 30m
cornus auth keys
cornus auth delete-key SHA256:abc123...
```

通过 `CORNUS_AUTHORIZED_KEYS` 提供的密钥属于声明式配置，不能通过 API 删除。删除密钥会阻止签发新会话；已经签发的无状态 JWT 在其短期有效期结束前仍然有效。

**另请参阅:** [安全与认证](/zh/guides/security)、[cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)
