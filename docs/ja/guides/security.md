# セキュリティと認証

Cornus の HTTP API (`/v2/*`、`/.cornus/v1/*`) は、既定では **認証なし**で提供されます。認証が設定されていない場合、ポートに到達できる誰もがイメージのプッシュ / プル、ビルドの実行、デプロイメントの作成を行えます。Cornus は信頼できるネットワーク上、認証を行うリバースプロキシの背後、または下記の組み込みベアラー認証を有効化した状態でだけ実行してください。このページのセキュリティ機能はいずれも明示的に有効化する方式で、無効なら余計な負荷はかかりません。関連する環境変数が何も設定されていなければ、サーバーは以前と完全に同じように動作し、要求ごとの追加負荷はありません。

上の段落は素の `cornus serve` にそのまま当てはまります。既定の listen アドレスが **`:5000`、つまり全インターフェース**だからです。そうである必要があります。コンテナ化された caretaker が、クライアントローカルマウント、クライアント側エグレス、資格情報の配布、ワークロードテレメトリーのためにサーバーへ逆方向に接続するからで、ホストの `127.0.0.1` はコンテナのそれではないため、ループバックのみに bind するとこれらの機能が壊れます。bind 範囲と認証を有効にすることは、一組の判断として扱ってください。

ワークロードがサーバーに到達する必要が**ない**場合は、`--addr 127.0.0.1:5000` (`CORNUS_ADDR=127.0.0.1:5000`) でこのマシンだけに制限できます。制限した場合はサーバーが起動ログでその旨を伝えます。[listen アドレスと公開範囲](/ja/cli/serve#listen-アドレスと公開範囲) を参照してください。

TLS は `--tls-cert` / `--tls-key` (または `CORNUS_TLS_CERT` / `CORNUS_TLS_KEY`) によりプロセス内で提供できます。ただし、これは転送経路の暗号化を提供するものであり、呼び出し元の認証ではありません。

## 仕組み

### ベアラー認証

ベアラー認証は、少なくとも 1 つのクライアント検証器が設定されると有効になります。有効化されると、`/healthz` と `/readyz` (常に開放)、および匿名プルが有効な場合の `/v2/*` 下の `GET` / `HEAD` を除き、すべての要求に有効な `Authorization: Bearer <token>` が必要です。Cornus はクライアントトークンを検証し、汎用の HTTP トークン発行サービスは公開しません。3 種類の検証器 (不透明な共有シークレット、対称鍵または非対称鍵の JWT、JWKS のキーセット) を組み合わせられ、いずれかがトークンを検証すれば要求は受け付けられます。

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

クライアント認証が有効な場合、Cornus は自身のデータプレーン用に独立したインストール署名キー (`CORNUS_INSTALLATION_SECRET`、または `$CORNUS_DATA/installation.key`) も作成します。インプロセスビルドには 15 分間の `registry:push` 資格情報を発行し、dockerhost、containerd、bare、Incus のプルには 15 分間の `registry:pull` 資格情報を発行します。Kubernetes では、12 時間有効な資格情報を持つ名前空間スコープのプルシークレット `cornus-registry-pull` を作成し、4 時間ごとに更新します。資格情報を発行するのは、サーバー自身の通知済みまたはループバックのレジストリホストだけであり、外部レジストリには決して発行しません。インストールキーはクライアント認証を有効にせず、クライアント資格情報として受け付けられず、HTTP エンドポイントでも公開されません。

Helm chart はこのインストールキーを共有シークレットとして用意し、Helm `lookup` でアップグレード時にも実際の値を維持します。そのため、クラスターを参照できない単独の `helm template` は、レンダーするたびに新しいランダム値を表示します。これはレンダー出力上の差だけであり、インストール済みキーのローテーションではありません。

### レプリカ間転送

クライアント認証と分散型ハブストア (`CORNUS_HUB_REDIS` または `CORNUS_HUB_STORE=kube`) を併用すると、各サーバーレプリカは `$CORNUS_DATA/peer.key` にモード `0600` の ECDSA P-256 秘密鍵を作成します。公開するのは公開鍵だけです。Redis ではレプリカのハートビート TTL で保持し、Kubernetes ストアではレプリカの Lease に所有させるため、離脱したレプリカのキーはルーティングレコードと同じライフサイクルで失効します。認証が無効な場合と、メモリ内ストアを使う単一レプリカサーバーでは、レプリカ間キーを作成しません。

`/.cornus/v1/hub/forward`、`/.cornus/v1/mount/forward`、`/.cornus/v1/cred/forward` では、`CORNUS_AUTH_TOKEN` を持たない送信元が 5 分間有効な ES256 JWT を発行してキャッシュします。スコープは `peer` で、`sub` と `kid` はどちらもレプリカ ID です。受信側はハブストアから `kid` を解決し、ES256 だけを受け付け、`sub == kid` を必須とします。`peer` スコープが到達できるのはこの 3 つの転送エンドポイントだけです。クライアント API、レジストリの読み書き、caretaker としての attach には使えません。

明示的な `CORNUS_AUTH_TOKEN` は絶対的な優先順位を維持し、そのまま送信されます。古いレプリカは `peer` スコープを理解しないため、これによりバージョンが混在するローリング更新を維持できます。したがってレプリカ間資格情報が有効になるのは、これまで資格情報を持てなかった JWT、JWKS、または mTLS のみを使う複数レプリカ構成だけです。

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

## SSH 公開鍵セッションを使う

SSH 鍵認証では、サーバーが保存するのは認可済み公開鍵だけです。クライアントはサーバーが発行した用途固定のチャレンジに署名し、Cornus SSH 鍵用に固定された発行者と対象者を持つ短時間有効な JWT を受け取ります。サーバーのインストールシークレットがこのセッションに署名し、運用者用の JWT 鍵とは独立しています。

単一サーバー用の書き込み可能なキーストアを有効にし、ローカルの登録コードを取得してクライアント鍵を登録します。

```sh
# サーバー (この設定がなければ、認証なしという既定の状態は変わりません):
CORNUS_AUTH_KEYSTORE=file cornus serve

# サーバー uid としてローカルで、または ssh/docker exec/kubectl exec 経由で実行:
code=$(cornus auth enrollment-code)

# クライアント:
cornus auth enroll --server https://cornus.example \
  --identity-file ~/.ssh/id_ed25519 --name laptop --code "$code"
```

登録に成功するとコードはローテーションされます。実行時の鍵はモード `0600` の `<CORNUS_DATA>/auth/authorized_keys` に保存されます。宣言的なインストールや複数レプリカのインストールでは、改行区切りの `CORNUS_AUTHORIZED_KEYS` と `CORNUS_AUTH_KEYSTORE=none` を設定してください。この場合、登録は `409` を返し、運用者を環境変数の設定へ案内します。Helm chart は `replicas > 1` のとき自動的に `none` を選択します。

署名器の選択情報を接続プロファイルに保存します。

```sh
cornus config set-context prod --server https://cornus.example \
  --key-auth-identity-file ~/.ssh/id_ed25519 --key-auth-name laptop
cornus config use-context prod
cornus auth keys
```

プロファイルが保存するのはパスと公開フィンガープリントであり、秘密鍵の内容ではありません。通常のコマンドは 1 時間有効な `api` セッションを発行し、非公開でキャッシュします。要求できる最長の有効期間は 24 時間です。優先順位は引き続き `CORNUS_TOKEN` が最上位で、鍵認証、kube 認証、静的プロファイルトークンの順です。1 つのプロファイルに `key-auth` と `kube-auth` を組み合わせることはできません。

`POST /.cornus/v1/auth/enroll` と `POST /.cornus/v1/auth/token` は、同じ 2 段階のハンドシェイクを使います。署名のない最初のリクエストはステートレスなチャレンジとともに `401` を返し、クライアントはそれに署名して同じエンドポイントを再試行します。チャレンジと証明は公開鍵および登録またはトークン要求の全フィールドに結び付けられるため、署名済みリクエストを転送中に変更することはできません。鍵認証が有効な場合、この 2 つの正確なルートだけが認証を免除されます。`GET` と `DELETE /.cornus/v1/auth/keys` には全権限の資格情報が必要です。RSA は SHA-2 署名を使い、RSA/SHA-1 と DSA は拒否します。鍵を削除すると新しいセッションは発行できなくなりますが、すでに発行済みのステートレスセッションは自然に期限切れになります。

**関連項目:** [cornus auth](/ja/cli/auth)、[接続設定](/ja/reference/connection-config)

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

- `--scope api` は完全な資格情報、`--scope registry:push` はレジストリの読み書き、`--scope registry:pull` はレジストリの読み取りだけを許可し、`--scope caretaker` は `/.cornus/v1/caretaker/attach` に制限されます。スコープは許可リストであり、フェイルクローズで動作します。これらの名前を一つも持たないトークン (`scope` クレーム自体がないものを含む) は、理由を示す 401 とともにすべてのエンドポイントで拒否され、`cornus token issue` もそのようなトークンを発行しません。
- `scope` クレームが決定権を持つのは、**あなたが署名鍵を保持している場合**だけです。つまりインストール秘密鍵、`CORNUS_JWT_HS256_SECRET`、`CORNUS_JWT_PUBLIC_KEY` のいずれかです。JWKS 経由で検証されるトークンはサードパーティのものなので、その `scope` は単独では何も付与しません。後述の [スコープマッピング](#サードパーティのクレームを-cornus-のスコープにマッピングする) を参照してください。
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

## サードパーティのクレームを cornus のスコープにマッピングする

JWKS が指すのは**他者の**キーセットです。相手のトークンを検証することはできますが、発行することはできません。したがって、そうしたトークンの `scope` クレームはその発行者の主張であって、あなたの主張ではありません。これを尊重してしまうと、*ID* の証明のために信頼している発行者が、`scope: api` を発行することで、あるいは自身の語彙で「scope」という語をまったく別の意味に使うことで、自らに*権限*を付与できてしまいます。

そのため cornus は、サードパーティのトークンが何に到達できるかを、あなたが記述する**スコープマップ**から決定します。これは `scope` クレームを持たず、付与することもできない Kubernetes ServiceAccount トークンに何かを付与する唯一の方法でもあります。

```yaml
# /etc/cornus/scopes.yaml — 順序付き。最初に一致したルールが有効。一致しなければ何も付与しない。
rules:
  - name: the deploy robot is an operator
    scope: api
    match:
      sub: { prefix: "system:serviceaccount:cornus-system:" }

  - name: CI pushes images
    scope: registry:push
    match:
      aud: { equals: cornus }
      "kubernetes.io/serviceaccount/namespace": { equals: ci }

  - name: verified staff read the registry
    scope: registry:pull
    match:
      email: { suffix: "@example.com" }
      email_verified: { equals: true }
```

```sh
CORNUS_JWT_JWKS_URL=https://issuer.example/.well-known/jwks.json \
CORNUS_JWT_SCOPE_MAP=/etc/cornus/scopes.yaml cornus serve
```

- 各ルールの `match` は**論理積**です。すべてのクレームが一致しなければなりません。ポリシーを広げるにはルールを追加し、狭めるにはルールにクレームを追加します。
- マッチャーは `equals`、`prefix`、`suffix`、`glob` (`path.Match`。`*` は `/` をまたぎません)、`any_of`、`contains` (JSON 配列または空白区切り文字列に対して) です。**正規表現は意図的にありません**。許可リストの中でアンカーされていないパターンは、書いた人が読んだ以上のものを付与してしまいます。
- クレームはまず**リテラルな名前**で探索され (`kubernetes.io/serviceaccount/namespace` はパスではなく 1 つのクレームです)、次にネストしたオブジェクトへのドット区切りパスとして探索されます (`kubernetes.io.pod.name`)。
- 不正なマップ、未知のスコープ、`match` が空のルール、テストを 1 つも指定していないマッチャーは、**起動時にサーバーを停止させます**。読み込みに黙って失敗したポリシーは、起動を拒否するサーバーよりも悪い結果になります。
- `CORNUS_JWT_DEFAULT_SCOPE=api` は単一のキャッチオールルールに相当し、マップの**後ろ**に追加されるため、明示的なルールが引き続き決定権を持ちます。検証器が受け入れるすべてのトークンが本当に完全な資格情報である場合に使ってください。要点は、これがクレームの欠落から推測されるのではなく、*明示される*ようになったということです。
- トークン自身の `scope` クレームも他のクレームと同様にマッチ可能なので、cornus のスコープを発行するよう意図的に設定した発行者は、明示的に尊重できます: `match: {iss: {equals: "https://idp.example.com"}, scope: {contains: "registry:pull"}}`

**関連項目:** [サーバー環境変数](/ja/reference/server-env-vars)、[リモートクラスター](/ja/guides/remote-clusters)

## サードパーティのトークンを Cornus の資格情報と交換する

`POST /.cornus/v1/auth/exchange` は [OAuth 2.0 Token Exchange (RFC 8693)](https://www.rfc-editor.org/rfc/rfc8693) を実装します。Cornus が検証できるトークンを提示すると、そのスコープを明示した短命の Cornus 資格情報が返されます。このエンドポイントは JWT または JWKS の検証器が設定されている場合にのみ現れます。

```sh
curl -s -X POST https://cornus.example.com/.cornus/v1/auth/exchange \
  -d grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
  -d subject_token_type=urn:ietf:params:oauth:token-type:jwt \
  -d subject_token="$(kubectl create token cornus-client --audience cornus)" \
  -d scope=registry:pull
```

```json
{
  "access_token": "eyJhbGciOi...",
  "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
  "token_type": "Bearer",
  "expires_in": 3600,
  "scope": "registry:pull"
}
```

これは直接パスと同じポリシーを、要求ごとではなく一度だけ適用したものです。subject トークンは同じ検証器を通り、同じスコープマップが決定し、返される資格情報は Cornus が発行したもので自身のスコープを明示します。したがって要求パス上で何かを推測する必要はありません。

- **`scope` は狭めることしかできません。** 省略すればマップが付与したものがそのまま得られます。より少なく要求すればより少なく得られます。マップが付与していないものを要求すると `invalid_scope` で拒否されます。要求パラメーターがポリシーを超えることは決してありません。
- **`caretaker` と `peer` は決して発行されません。** マップがそれらを付与していても、クライアントがそこへ狭めるよう要求しても同じです。どちらもクライアント用の資格情報ではありません。`caretaker` は直接パスでそれを提示するサイドカーのものであり、`peer` はハブストアに公開された鍵に対して検証されるサーバー間の資格情報です。クライアントはそのどちらの側にもいません。
- トークンの寿命は 1 時間で、発行者と対象者 `cornus:exchange` を持つため、監査証跡において運用者が発行したトークンと区別できます。各交換は subject、一致したルール、発行されたスコープを示す 1 行を記録します。要求ごとではなく資格情報ごとに 1 レコードです。
- 委譲 (`actor_token`) は無視されるのではなく拒否されます。

**関連項目:** [スコープマッピング](#サードパーティのクレームを-cornus-のスコープにマッピングする)

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
| `activity` | `GET /.cornus/v1/activity` (サーバーアクティビティのフライトレコード) |
| `observe` | オブザーバビリティの取り込み、クエリ、Grafana プロキシの各エンドポイント |
| `tunnel` | 公開イングレストンネルの作成と操作 (`deploy` でも暗黙的に許可) |

未設定ならすべて許可されます。設定後は、呼び出し元がその操作 (または `"*"`) に記載されている必要があり、**空の ID は拒否されます (フェイルクローズド)**。そのためポリシーには ID を持つ資格情報 (JWT `sub` または mTLS CommonName) が必要です。不透明な静的トークンと匿名の呼び出し元は拒否されます。不正な JSON は起動時の致命的なエラーです。読み取り専用エンドポイントの大半には個別の操作ゲートがありませんが、アクティビティログとオブザーバビリティの各機能は上記のとおり制限されます。レジストリプルは、規則が明示的に有効化した場合だけ制限されます。認証を有効にした場合は、文書化されているヘルス / readiness および匿名プルの例外を除くすべてのエンドポイントに対し、ポリシーとは独立して引き続き適用されます。

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
