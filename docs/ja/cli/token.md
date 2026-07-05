# cornus token

ベアラー認証を使用する Cornus サーバー向けに署名付き JWT を発行します。

## 構文

```sh
cornus token issue [flags]
```

## 説明

Cornus サーバーは検証専用です。`cornus token issue` は、サーバーが検証するトークンを発行するプロセス内の発行者です。サーバーが検証するものと同じ鍵材料で署名します。両側に設定する HS256 シークレット (`CORNUS_JWT_HS256_SECRET`)、または公開部分をサーバーの `CORNUS_JWT_PUBLIC_KEY` に設定した秘密鍵を使用します。発行されたトークンは標準出力に表示されます。

サーバーによるトークン検証と claim からアクセスへの対応は、[セキュリティと認証](/ja/guides/security)を参照してください。

## cornus token issue

署名付き JWT を一つ発行します。

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--sub` | — | — | Subject (`sub`) claim。呼び出し元の ID。 |
| `--scope` | — | `api` | 範囲: `api` (完全アクセス。空の範囲の既定の意味) または `caretaker` (caretaker エンドポイントのみ)。 |
| `--ttl` | — | `1h` | トークンの有効期間 (例: `1h`、`720h`)。 |
| `--iss` | — | — | Issuer (`iss`) claim。設定時はサーバーの `CORNUS_JWT_ISSUER` と一致する必要があります。 |
| `--aud` | — | — | Audience (`aud`) claim。設定時はサーバーの `CORNUS_JWT_AUDIENCE` と一致する必要があります。 |
| `--kid` | — | — | キー ID ヘッダー。JWKS verifier (`CORNUS_JWT_JWKS_FILE`/`_URL`) が対応する鍵を選べるようにします。 |
| `--hs256-secret` | `CORNUS_JWT_HS256_SECRET` | — | HMAC シークレット (対称鍵)。サーバーは同じシークレットで検証します。最低 32 バイト。 |
| `--private-key` | — | — | PEM 秘密鍵ファイル (RSA では RS256、ECDSA では ES256)。サーバーは対応する公開鍵で検証します。 |

## 例

対称シークレットによる HS256 署名トークンを発行します。

```sh
export CORNUS_JWT_HS256_SECRET='a-secret-at-least-32-bytes-long!!'
cornus token issue --sub alice --ttl 24h
```

秘密鍵で署名する非対称トークンを発行します。

```sh
cornus token issue --sub ci --scope api --private-key ./signing-key.pem --kid key-1
```

caretaker 範囲のトークンを発行します。

```sh
cornus token issue --sub sidecar --scope caretaker --aud cornus
```
