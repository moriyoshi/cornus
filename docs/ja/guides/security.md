# セキュリティと認証

Cornus の HTTP API (`/v2/*`、`/.cornus/v1/*`) は、既定では **認証なし**で提供されます。認証が設定されていない場合、ポートに到達できる誰もがイメージのプッシュ / プル、ビルドの実行、デプロイメントの作成を行えます。Cornus は信頼できるネットワーク上、認証を行うリバースプロキシの背後、または下記の組み込みベアラー認証を有効化した状態でだけ実行してください。このページのセキュリティ機能はいずれも明示的に有効化する方式で、無効なら余計な負荷はかかりません。関連する環境変数が何も設定されていなければ、サーバーは以前と完全に同じように動作し、要求ごとの追加負荷はありません。

TLS は `--tls-cert` / `--tls-key` (または `CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`) によりプロセス内で提供できます。ただし、これは転送経路の暗号化を提供するものであり、呼び出し元の認証ではありません。

## 仕組み

### ベアラー認証

ベアラー認証は、少なくとも 1 つの検証器が設定されると有効になります。有効化されると、`/healthz` と `/readyz` (常に開放)、および匿名プルが有効な場合の `/v2/*` 下の `GET` / `HEAD` を除き、すべての要求に有効な `Authorization: Bearer <token>` が必要です。Cornus はトークンを**検証**するだけで、発行はしません。3 種類の検証器 (不透明な共有シークレット、対称鍵または非対称鍵の JWT、JWKS のキーセット) を組み合わせられ、いずれかがトークンを検証すれば要求は受け付けられます。

任意の JWT クレーム検証は設定されている場合にだけ実施されます。`CORNUS_JWT_ISSUER` はトークンの `iss` と一致する必要があり、`CORNUS_JWT_AUDIENCE` はトークンの `aud` と一致する必要があります。`exp` と `nbf` は常に 1 分の猶予を付けて検証され、`alg: none` または想定外のアルゴリズムを持つトークンは拒否されます。完全な環境変数一覧は[サーバー環境変数](/ja/reference/server-env-vars)にあります。

### 呼び出し元 ID

呼び出し元の認証 ID、つまり mTLS CommonName または JWT `sub` は統一して扱われます。どちらも同じ ID ごとの認可ポリシーに使われます。不透明な静的トークン (`CORNUS_AUTH_TOKEN`) は **ID を持たず**、匿名として扱われます。

### クライアント側

Cornus CLI と `pkg/client` は `CORNUS_TOKEN` を読み、`/.cornus/v1/*` の呼び出し、アーカイブの `PUT`、WebSocket の attach ハンドシェイク (デプロイの attach、ビルド、exec) で `Authorization: Bearer <token>` として送ります。

```sh
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

認証が有効なときに外部 OCI クライアントが `/v2/*` にアクセスする場合、`cornus push` は `CORNUS_TOKEN` をレジストリのベアラー資格情報として送ります。標準の `docker` / `podman` / `crane` は通常の `docker login` でログインします。レジストリは `/v2/*` で HTTP Basic を受け付けます。パスワードがトークン (静的トークンまたは JWT) で、ユーザー名は無視されます。401 チャレンジは `Basic realm="cornus"` なので、トークンサービスなしで標準のログインフローが動きます。

```sh
docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"
```

**関連項目:** [cornus serve](/ja/cli/serve)、[サーバー環境変数](/ja/reference/server-env-vars)

## 静的ベアラートークンを必須にする

単一の不透明な共有シークレットでベアラー認証を有効にします。

```sh
# サーバー: 検証器を設定すると直ちに適用される。
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) cornus serve

# クライアント: /.cornus/v1/* と /v2/* には Authorization: Bearer <token> として送信される。
CORNUS_TOKEN=<token> cornus deploy -f app.yaml --server https://cornus.example
```

- `/healthz` と `/readyz` は開いたままで、その他の要求にはトークンが必要です。
- 静的トークンは**ID を持たず**匿名として扱われるため、ID ごとのポリシー (後述) を満たせません。通常の OCI クライアントでは `docker login cornus.example:5000 -u token -p "$CORNUS_TOKEN"` を使います。

**関連項目:** [cornus serve](/ja/cli/serve)

## クライアント用の JWT を発行する

サーバーはトークンを検証するだけです。`cornus token issue` で、同じキー素材を用いてサーバーが受け入れる JWT を発行します。

```sh
# 対称 (HS256): サーバーは同じシークレットで検証する。
export CORNUS_JWT_HS256_SECRET="$(openssl rand -hex 32)"   # >= 32 bytes
cornus token issue --sub ci-bot --scope api --ttl 1h --hs256-secret "$CORNUS_JWT_HS256_SECRET"

# 非対称: 秘密鍵で発行し、サーバーには公開鍵だけを置く。
cornus token issue --sub pod-x --scope caretaker --ttl 720h --private-key ./jwt-priv.pem
#   サーバー側: CORNUS_JWT_PUBLIC_KEY=./jwt-pub.pem cornus serve
```

- `--scope api` (または空) は完全な資格情報、`--scope caretaker` は `/.cornus/v1/caretaker/attach` に制限されます。
- `--sub` は以下のポリシーで使用する呼び出し元 ID になります。設定時は `--iss` / `--aud` が `CORNUS_JWT_ISSUER` / `CORNUS_JWT_AUDIENCE` と一致する必要があります。
- 鍵の種類がアルゴリズムを決めます (RSA なら RS256、ECDSA なら ES256)。公開鍵に対して HS256 が受け付けられることは決してないため、この構成はアルゴリズム混同に対して安全です。

**関連項目:** [cornus token](/ja/cli/token)

## JWKS エンドポイントに対してトークンを検証する

公開されたキーセットに対して非対称 JWT を検証します。`kid` による選択とローテーションをサポートします。

```sh
# リモート JWKS: キャッシュし、TTL 到達時と未知の kid が指定された場合にレート制限付きで再取得する。
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json cornus serve

# ローカル JWKS ファイル: 変更時にホットリロードする。
CORNUS_JWT_JWKS_FILE=/etc/cornus/jwks.json cornus serve
```

- 非対称アルゴリズムだけを受け付けます。トークンの `kid` ヘッダーがキーを選びます。発行時は `cornus token issue --kid <id> --private-key key.pem ...` で対応する ID を付けます。
- `exp` / `nbf` は常に検証されます (1 分の猶予)。`alg: none` または予期しないアルゴリズムは拒否されます。

**関連項目:** [cornus token](/ja/cli/token)

## mTLS を有効にし、クライアント証明書から ID を導出する

TLS で提供している場合、Cornus は **クライアント証明書** による呼び出し元認証も行えます。これはベアラートークンと並ぶ追加の方式であり、置き換えではありません。`--tls-client-ca` (または `CORNUS_TLS_CLIENT_CA`) に PEM CA バンドルを指定します。

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- 提示した証明書は `--tls-client-ca` に連なる必要があります。検証済みの `Subject.CommonName` が ID になります。証明書の提示自体は**任意**のままです (リスナーは `VerifyClientCertIfGiven` を使うため、`/healthz`、`/readyz`、ベアラー認証のみを使うクライアントも動作します) が、提示された証明書は必ず検証されます。
- 検証済みクライアント証明書は完全な資格情報であり、同じ要求のベアラートークンより**優先**されます。`--tls-client-ca` (または `CORNUS_TLS_CLIENT_CA`) の設定だけでも認証を有効にします。

**関連項目:** [インストール](/ja/introduction/installation)

## ID ごとに操作を認可する

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

**関連項目:** [サーバー環境変数](/ja/reference/server-env-vars)

## 書き込みを保護したまま匿名レジストリプルを許可する

プッシュ、ビルド、デプロイは認証の背後に置きつつ、誰でもイメージをプルできるようにします。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- これは `GET` / `HEAD` だけを `/v2/*` 下で開きます。すべての書き込みメソッドには資格情報が必要です。このフラグは `1`/`true`/`yes`/`on` を受け付けます。
- `pull` 規則を `CORNUS_API_POLICY` で明示すると、このフラグより優先します (両方設定すると起動時警告)。`pull` 規則がなければレジストリプルは認証で決まり、二つは競合しません。

**関連項目:** [レジストリとストレージ](/ja/guides/registry)

## スコープを限定した caretaker 資格情報を理解する

Pod ごとの caretaker が到達するのは `/.cornus/v1/caretaker/attach` だけなので、完全なトークンではなく **独立した用途限定の** トークンが与えられます。認証下で Kubernetes バックエンドを動かす場合、クライアント認証と一緒に設定してください。バックエンドはマウント / hub サイドカーに自動注入します。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # 別々のシークレット
```

- サーバーは caretaker エンドポイントだけで caretaker トークンを受け入れ、クライアント API とレジストリでは拒否します。そのため Pod 仕様から読み取られたサイドカー資格情報はデプロイ、ビルド、exec、プッシュを実行できません。
- 不透明な `CORNUS_CARETAKER_TOKEN`、または `caretaker` スコープの JWT (`cornus token issue --scope caretaker`) を使えます。そのため静的トークンをまったく持たない JWT 専用サーバーでも、Kubernetes のライブマウントに対応できます。トークンを Pod 仕様から外すには Kubernetes シークレットに保存し、`CORNUS_CARETAKER_TOKEN_SECRET` で Cornus に指定します。サイドカーはランタイムに `secretKeyRef` 経由でトークンを取得します。

**関連項目:** [サーバー環境変数](/ja/reference/server-env-vars)
