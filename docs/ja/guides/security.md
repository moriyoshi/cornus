# サーバーを保護する

Cornus の HTTP API (`/v2/*`、`/.cornus/v1/*`) は、既定では**認証なし**です。ポートへ到達できる人は誰でもプッシュ、ビルド、デプロイできます。以下の制御はすべて任意で、有効にしなければ余計な負荷はかかりません。Cornus は信頼できるネットワーク上、認証プロキシの背後、またはここで説明する認証を有効にして実行してください。詳しい仕組みは[認証と TLS](/ja/topics/auth-and-tls)を参照してください。

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

**関連項目:** [認証と TLS](/ja/topics/auth-and-tls)、[cornus serve](/ja/cli/serve)

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

**関連項目:** [cornus token](/ja/cli/token)、[認証と TLS](/ja/topics/auth-and-tls)

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

**関連項目:** [認証と TLS](/ja/topics/auth-and-tls)、[cornus token](/ja/cli/token)

## mTLS を有効にし、クライアント証明書から ID を導出する

CommonName が呼び出し元 ID になるクライアント証明書で、呼び出し元を認証します。

```sh
cornus serve --tls-cert server.pem --tls-key server-key.pem \
  --tls-client-ca client-ca.pem
```

- 提示した証明書は `--tls-client-ca` に連なる必要があります。検証済みの `Subject.CommonName` が ID になります。証明書の提示自体は**任意**のままです (ベアラー認証のみを使うクライアントとプローブクライアントも動作します) が、提示された証明書は必ず検証されます。
- 検証済みクライアント証明書は完全な資格情報であり、同じ要求のベアラートークンより**優先**されます。`--tls-client-ca` (または `CORNUS_TLS_CLIENT_CA`) の設定だけでも認証を有効にします。

**関連項目:** [認証と TLS](/ja/topics/auth-and-tls)、[インストール](/ja/introduction/installation)

## ID ごとに操作を認可する

どの ID がどの API 操作を実行できるかを制限します。

```sh
CORNUS_API_POLICY='{"ci-bot":["deploy","build","push"],"admin":["*"]}' cornus serve
```

- 操作は `deploy` (`exec` を含む)、`exec`、`build`、`push`、`pull`、`gc` です。`"*"` はすべてを許可します。
- 未設定ならすべてを許可します。設定後は呼び出し元が操作のために列挙されていなければならず、**空の ID は拒否されます (フェイルクローズ)**。したがってポリシーには ID を提供する資格情報 (JWT `sub` または mTLS CommonName) が必要です。静的トークンと匿名呼び出し元は拒否されます。不正な JSON は起動時に致命的なエラーになります。

**関連項目:** [認証と TLS](/ja/topics/auth-and-tls)、[サーバー環境変数](/ja/reference/server-env-vars)

## 書き込みを保護したまま匿名レジストリプルを許可する

プッシュ、ビルド、デプロイは認証の背後に置きつつ、誰でもイメージをプルできるようにします。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve
```

- これは `GET` / `HEAD` だけを `/v2/*` 下で開きます。すべての書き込みメソッドには資格情報が必要です。
- `pull` 規則を `CORNUS_API_POLICY` で明示すると、このフラグより優先します (両方設定すると起動時警告)。`pull` 規則がなければレジストリプルは認証で決まり、二つは競合しません。

**関連項目:** [レジストリとストレージ](/ja/guides/registry)、[認証と TLS](/ja/topics/auth-and-tls)

## スコープを限定した caretaker 資格情報を理解する

Pod ごとの caretaker (サイドカー) は `/.cornus/v1/caretaker/attach` にしか到達しないため、完全なトークンではなく別のスコープ限定トークンを渡します。

```sh
CORNUS_AUTH_TOKEN=$(openssl rand -hex 32) \
CORNUS_CARETAKER_TOKEN=$(openssl rand -hex 32) cornus serve   # 別々のシークレット
```

- サーバーは caretaker エンドポイントだけで caretaker トークンを受け入れ、クライアント API とレジストリでは拒否します。そのため Pod 仕様から読み取られたサイドカー資格情報はデプロイ、ビルド、exec、プッシュを実行できません。
- 不透明な `CORNUS_CARETAKER_TOKEN`、または `caretaker` スコープの JWT (`cornus token issue --scope caretaker`) を使えます。JWT のみを受け付けるサーバーでも Kubernetes のライブマウントをサポートできます。Pod 仕様に入れないにはシークレットに保存し、`CORNUS_CARETAKER_TOKEN_SECRET` で参照します。

**関連項目:** [認証と TLS](/ja/topics/auth-and-tls)、[サーバー環境変数](/ja/reference/server-env-vars)
