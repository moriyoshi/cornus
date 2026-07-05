# 認証と TLS

Cornus の HTTP API (`/v2/*`、`/.cornus/v1/*`) は、既定では **認証なし**で提供されます。認証が設定されていない場合、ポートに到達できる誰もがイメージのプッシュ / プル、ビルドの実行、デプロイメントの作成を行えます。Cornus は信頼できるネットワーク上、認証を行うリバースプロキシの背後、または下記の組み込みベアラー認証を有効化した状態でだけ実行してください。このページのセキュリティ機能はいずれも明示的に有効化する方式で、無効なら余計な負荷はかかりません。関連する環境変数が何も設定されていなければ、サーバーは以前と完全に同じように動作し、要求ごとの追加負荷はありません。

TLS は `--tls-cert` / `--tls-key` (または `CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`) によりプロセス内で提供できます。ただし、これは転送経路の暗号化を提供するものであり、呼び出し元の認証ではありません。

## ベアラー認証

ベアラー認証は、少なくとも 1 つの検証器が設定されると有効になります。有効化されると、`/healthz` と `/readyz` (常に開放)、および匿名プルが有効な場合の `/v2/*` 下の `GET` / `HEAD` を除き、すべての要求に有効な `Authorization: Bearer <token>` が必要です。Cornus はトークンを**検証**するだけで、発行はしません。3 種類の検証器を組み合わせられ、いずれかがトークンを検証すれば要求は受け付けられます。

```sh
# 1. Opaque shared secret (level 0, zero dependencies):
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# 2. Symmetric JWT (HS256). Use a secret of at least 32 bytes:
CORNUS_JWT_HS256_SECRET="$(openssl rand -hex 32)" cornus serve

# 3. Asymmetric JWT (RS256 or ES256) verified with a PEM public key. The key
#    type selects the algorithm (RSA -> RS256, ECDSA -> ES256); HS256 is never
#    accepted against a public key (algorithm-confusion-safe):
CORNUS_JWT_PUBLIC_KEY=/etc/cornus/jwt-pub.pem cornus serve

# 4. JWKS with kid selection + rotation, from a file (hot-reloaded) or a URL
#    (cached, refetched on TTL and, rate-limited, on an unknown kid):
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve
```

任意の JWT クレーム検証は設定されている場合にだけ実施されます。`CORNUS_JWT_ISSUER` はトークンの `iss` と一致する必要があり、`CORNUS_JWT_AUDIENCE` はトークンの `aud` と一致する必要があります。`exp` と `nbf` は常に 1 分の猶予を付けて検証され、`alg: none` または想定外のアルゴリズムを持つトークンは拒否されます。完全な環境変数一覧は[サーバー環境変数](/ja/reference/server-env-vars)にあります。

### JWT の発行

サーバーは検証するだけなので、サーバーが受け付ける JWT を発行するには `cornus token issue` を使います。サーバーが検証するものと同じ鍵素材で署名します。トークンの `scope` は許可範囲を決めます。`api` (または空の範囲) は全権限の資格情報です。`caretaker` は `/.cornus/v1/caretaker/attach` だけに制限されます。

```sh
# Symmetric (HS256) -- the server verifies with the same secret:
cornus token issue --sub ci-bot --scope api --ttl 1h \
  --hs256-secret "$CORNUS_JWT_HS256_SECRET"

# 非対称鍵: 秘密鍵で発行し、サーバーは公開鍵だけを保持する
cornus token issue --sub pod-x --scope caretaker --ttl 720h \
  --private-key ./jwt-priv.pem      # server: CORNUS_JWT_PUBLIC_KEY=./jwt-pub.pem
```

JWKS 検証器用に発行する場合は、対応するキー ID を `--kid <id>` で指定します。完全なフラグ一覧は[`cornus token`](/ja/cli/token)を参照してください。

### クライアント側

Cornus CLI と `pkg/client` は `CORNUS_TOKEN` を読み、`/.cornus/v1/*` の呼び出し、アーカイブの `PUT`、WebSocket の attach ハンドシェイク (デプロイの attach、ビルド、exec) で `Authorization: Bearer <token>` として送ります。

```sh
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

認証が有効なときに外部 OCI クライアントが `/v2/*` にアクセスする場合、`cornus push` は `CORNUS_TOKEN` をレジストリのベアラー資格情報として送ります。標準の `docker` / `podman` / `crane` は通常の `docker login` でログインします。レジストリは `/v2/*` で HTTP Basic を受け付けます。パスワードがトークン (静的トークンまたは JWT) で、ユーザー名は無視されます。401 チャレンジは `Basic realm="cornus"` なので、トークンサービスなしで標準のログインフローが動きます。

```sh
docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"
```

## mTLS クライアント証明書 ID

TLS で提供している場合、Cornus は **クライアント証明書** による呼び出し元認証も行えます。これはベアラートークンと並ぶ追加の方式であり、置き換えではありません。`--tls-client-ca` (または `CORNUS_TLS_CLIENT_CA`) に PEM CA バンドルを指定します。

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

提示されたクライアント証明書はその CA に連なる必要があります。検証済みの `Subject.CommonName` が呼び出し元 ID になります。証明書の提示は **任意** のままです (リスナーは `VerifyClientCertIfGiven` を使います)。そのため `/healthz`、`/readyz`、ベアラートークンだけを使うクライアントは証明書なしでも動き続けます。ただし提示された証明書は検証されなければなりません。検証済みのクライアント証明書は **完全な資格情報** であり、同じ要求上のベアラートークンより **優先** されます。`CORNUS_TLS_CLIENT_CA` の設定は、それだけで認証を有効化します。

呼び出し元の認証 ID、つまり mTLS CommonName または JWT `sub` は統一して扱われます。どちらも下記の ID ごとの認可に使われます。不透明な静的トークン (`CORNUS_AUTH_TOKEN`) は **ID を持たず**、匿名として扱われます。

## ID ごとの認可ポリシー

`CORNUS_API_POLICY` は、どの ID がどの API 操作を実行できるかを制限します。ID から許可する操作のリストへの JSON オブジェクトで、項目には `"*"` を使ってすべての操作を許可できます。

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

| 操作 | 対象 |
| --- | --- |
| `deploy` | デプロイメントの作成 / 削除と、状態を変更するライフサイクル / attach 操作 (`exec` を含む) |
| `exec` | 実行中のデプロイメントへの exec / attach (`exec` だけを許可する項目は、デプロイ権限なしでシェルを許可) |
| `build` | `POST /.cornus/v1/build` |
| `push` | `/v2/*` 下のレジストリへの書き込み (イメージのプッシュと削除) |
| `pull` | レジストリの `GET` / `HEAD`。明示的に有効化する方式で、規則が `pull` に明示的に言及した場合だけ強制されます (`"*"` は数えません) |
| `gc` | 破壊的な `POST /.cornus/v1/gc` の再利用エンドポイント |

未設定ならすべて許可されます。設定後は、呼び出し元がその操作 (または `"*"`) に記載されている必要があり、**空の ID は拒否されます (フェイルクローズド)**。そのためポリシーには ID を持つ資格情報 (JWT `sub` または mTLS CommonName) が必要です。不透明な静的トークンと匿名の呼び出し元は拒否されます。不正な JSON は起動時の致命的なエラーです。読み取り専用の `GET` エンドポイントは、レジストリプルを除いて制限されません。レジストリプルも規則が明示的に有効化した場合だけです。

## レジストリの運用方針と匿名プル

認証が有効な場合、`/v2/*` はすべての HTTP メソッドで認証を要求します。プッシュ / 削除には認証を要求し続けつつ、認証なしのプル (`GET` / `HEAD`) を許可するには次のようにします。

```sh
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve   # 1/true/yes/on
```

`CORNUS_API_POLICY` の明示的な `pull` 規則は `CORNUS_REGISTRY_ANONYMOUS_PULL` に優先します (両方が設定された場合は起動時に警告します)。`pull` に言及する規則がない場合、レジストリプルはポリシーではなく認証によって管理されるため、両者は競合しません。

## caretaker の資格情報

Pod ごとの caretaker が到達するのは `/.cornus/v1/caretaker/attach` だけなので、完全なトークンではなく **独立した用途限定の** トークンが与えられます。サーバーは caretaker エンドポイントでだけそれを受け付け、クライアント API とレジストリでは拒否します。そのため Pod 仕様からサイドカーの資格情報が読み取られても、デプロイ、ビルド、exec、プッシュはできません。認証下で Kubernetes バックエンドを動かす場合、クライアント認証と一緒に設定してください。バックエンドはマウント / hub サイドカーに自動注入します。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # distinct secrets
```

用途限定の caretaker 資格情報は、不透明な `CORNUS_CARETAKER_TOKEN` 文字列でも、`cornus token issue --scope caretaker` で発行した `caretaker` スコープの JWT でも構いません。そのため静的トークンをまったく持たない JWT 専用サーバーでも、Kubernetes のライブマウントに対応できます。トークンを Pod 仕様から外すには Kubernetes シークレットに保存し、`CORNUS_CARETAKER_TOKEN_SECRET` で Cornus に指定します。サイドカーはランタイムに `secretKeyRef` 経由でトークンを取得します。

サーバーフラグは [`cornus serve`](/ja/cli/serve) を、完全な configuration 対象範囲は [サーバー env vars](/ja/reference/server-env-vars) を参照してください。
