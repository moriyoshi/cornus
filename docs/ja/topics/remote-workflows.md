# リモートワークフロー

Cornus はローカルだけでなく、リモートサーバーやクラスター内のデプロイメントに対しても同じように利用できます。ビルドエンジン、レジストリ、デプロイエンジンはすべてサーバー上で動きます。一方でソース、シークレット、バインドマウントは自分のマシンに残り、必要に応じてストリーミングされます。このページでは、リモートの Cornus をローカルのように扱うための要素、つまりエンドポイントフラグ、リモートビルド、クライアントローカルマウント付きリモートデプロイ、接続プロファイル、セッションの通信経路、Kubernetes へのアクセス権からの資格情報発行をまとめます。

## CLI をサーバーに向ける

Cornus サーバーと通信するすべてのクライアントコマンドはエンドポイントを受け取り、ベアラートークンを環境から読みます。

| 設定 | 環境変数 | 使用するコマンド |
| --- | --- | --- |
| `--server` | `CORNUS_SERVER` | `deploy`, `exec`, `port-forward`, `socks5`, `tunnel`, `compose`, `hub`, ... |
| `--builder` | `CORNUS_BUILDER` | `build` (リモートビルドの attach エンドポイント) |
| `CORNUS_TOKEN` | `CORNUS_TOKEN` | `/.cornus/v1/*` のベアラー認証、アーカイブの `PUT`、WebSocket の attach |

コマンド上の明示的なエンドポイントが優先されます。なければ選択中の接続プロファイルから解決されます (後述)。エンドポイントには `http(s)://` または `ws(s)://` 形式を指定できます。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_TOKEN=<token> cornus exec -i -t web sh --server https://cornus.example.com
```

呼び出すたびにエンドポイントとトークンを入力し直すのはすぐに煩わしくなります。接続プロファイルを使えば、これらをコマンドラインから完全に取り除けます。

## リモートビルド

`cornus build --builder` は Cornus サーバー上でビルドを実行しながら、呼び出し元のコンテキスト、名前付きバインドディレクトリ、シークレット、SSH エージェントを **9P-on-WebSocket** でストリーミングします。ビルドは BuildKit ネイティブのままで、キャッシュはサーバー上に残ります。ホストに Docker やビルド権限は不要です。

```sh
cornus build --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 \
  --build-context data=./data \
  --secret id=token,src=./token.txt \
  --ssh default ./context
```

Dockerfile 内では、ストリームされた入力は通常の buildx マウントとして見えます。

```dockerfile
RUN --mount=type=bind,from=data ...
RUN --mount=type=secret,id=token ...
RUN --mount=type=ssh ...
```

呼び出し元の SSH エージェントは `type=ssh` マウントのために転送されるので、プライベートな依存関係の取得でもキーが自分のマシンから出ることはありません。

### 遅延コンテキスト

既定では名前付きコンテキストは事前に同期されます。`--lazy` (または `CORNUS_LAZY_BUILD`) を使うと、コンテキストは必要に応じて提供されます。そのためビルドが実際に読むバイトだけがネットワークを通過します。20 MB のコンテキストでビルドが 11 バイトだけ読むなら、転送されるのは 11 バイトです。遅延コンテキストは `CORNUS_BUILD_WORKER=containerd` では対応されません。

```sh
cornus build --lazy --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 --build-context data=./big-data ./context
```

`server` を持つプロファイルは、それだけでビルドをリモートに経路します (明示的な `--builder` は引き続き優先)。`type=local` でキー付けされたビルドキャッシュはファイルシステムパスではなく name を使うため、同じ `--cache-to` / `--cache-from` がローカルビルドとリモートビルドで同一に動きます。完全なフラグ set は [`cornus build`](/ja/cli/build) を参照してください。

## クライアントローカルバインドマウント付きリモートデプロイ

`cornus deploy --server` はリモートサーバー上でデプロイメントを実行しつつ、*この* machine 上にあるディレクトリを `--local-mount` (または Compose `volumes:`) で 9P ストリームして bind-mount します。デプロイメントはコマンドが接続している間だけ生きます。これが inner-loop ツールになる理由です。ローカルにファイルを編集するとワークロードがそれを見ます。

```sh
cornus deploy --server http://cornus.example:5000 \
  --local-mount ./config:/etc/app:ro --local-mount ./data:/data -f deploy.yaml
```

仕様の `ports:` にある公開済みポートは、セッション lifetime 中、自分の machine の `127.0.0.1:<host>` へ自動転送されます。バックエンドが Kubernetes クラスターであっても同じなので、ワークロードはローカルに応答します。opt out するには `--no-forward-ports` を使います。クライアントローカルマウントはサーバー自身の `<DataDir>/mounts` area から提供され、常に許可されます。そのため host-privilege ポリシーを緩めなくても動作します。[`cornus deploy`](/ja/cli/deploy) を参照してください。

9P を素朴に tunnel すると、すべての読み取りが wire を越えるため、大きい / 書き込みの多いマウントでは問題になります。2 つの suffix でサーバー側のファイルキャッシュを有効にできます。`,cache` (`:ro` を含意) はデータセットやモデルの重みのような **immutable** な入力向けの read-through cache で、`,async` は開発用データベースのような **single-writer** ワークロード向けの、書き込み可能で cache-coherent なマウントです。どちらもサーバーのファイルキャッシュを有効にする必要があります。`,async` マウントは、両端で `CORNUS_BLOCK_COHERENCE` / `CORNUS_BLOCK_READAHEAD` を設定することで、データベース型のランダム I/O 向けに tune できます。キャッシュの仕組みは [クライアントローカルバインドマウント](/ja/architecture/deploy-engine#クライアントローカルバインドマウント) を、各 knob は [サーバー環境変数](/ja/reference/server-env-vars#リモート-9p-ファイルキャッシュと書き込み可能マウント) を参照してください。

```sh
cornus deploy --server http://cornus.example:5000 \
  --local-mount ./models:/models:ro,cache \
  --local-mount ./db:/var/lib/app:async -f deploy.yaml
```

## 接続 profiles

`cornus config` は kubeconfig 風ファイルを管理します (既定は platform user 設定 dir、`--config-file` / `CORNUS_CONFIG` で上書き)。名前付き接続を一度保存しておけば、すべてのコマンドがコマンド line に何も指定せずそれを使います。

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod          # make it the default context

cornus config get-contexts              # list profiles (current is marked *)
cornus config view                      # print the file (bearer tokens redacted)

# Commands now need no --server / CORNUS_TOKEN:
cornus deploy -f app.yaml
cornus compose up
```

プロファイルは `deploy`、`exec`、`port-forward`、`socks5`、`tunnel`、`compose`、`daemon docker`、`build`、`hub` で尊重されます。エンドポイント precedence は明示的フラグ、次に selected コンテキストのサーバーです。トークン precedence は `CORNUS_TOKEN`、次にプロファイルトークンです。コマンドごとにプロファイルを選ぶには `--context <name>` (env `CORNUS_CONTEXT`) を使います。コンテキスト全体は JSON/YAML ファイルから読み込めます (`--from-file` は base レイヤー、`--from-file-override` はファイル側を勝たせる)。round-trip 用にエクスポートもできます (`config view --export`)。完全なフィールド set は [接続設定](/ja/reference/connection-config) に document されています。コマンド自体は [`cornus config`](/ja/cli/config) です。

### クラスターへの自動ポート転送

イングレスのないクラスター内 Cornus では、プロファイルに URL ではなく **サービス** を指定できます。すると CLI がコマンドごとの lifetime 中、自分でポート転送を開きます。組み込みな `kubectl port-forward` 相当です。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster

cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

バックグラウンドの `kubectl port-forward svc/cornus 5000:5000 &` も `--server` も不要です。転送は各コマンドの前後で設定 / 削除されます。`--pf-kube-context` は kubeconfig コンテキストを選びます。deployed **ワークロード** のポートに到達することは別の自動 concern です。セッションが publish する任意の `ports:` は `127.0.0.1:<host>` にトンネルされ、[`cornus port-forward`](/ja/cli/port-forward) は unpublished コンテナポートにも必要に応じて到達します。クラスタープロファイルでは、どちらも kubeconfig を使ってワークロード pod へ SPDY で直接向かい、必要なら Cornus サーバー経由のトンネルにフォールバックします。

## セッション conduits: ポート転送 vs SOCKS5

セッションがワークロードを呼び出し元に公開する方法が **conduit モード** です。既定はポートごとの転送です (公開済みポートごとにローカルリスナー 1 つ、Compose-compatible)。opt-in の代替は、単一のクライアント側 **SOCKS5 スプリットトンネルプロキシ** です。service-host 接尾辞 (既定 `.cornus.internal`) 配下の hostname は matching ワークロードに名前でトンネルされ、それ以外の宛先は自分の machine から直接接続されます。1 つのプロキシがサービスごとに名前で到達でき、ポートごとのリスナーは不要です。

```sh
# Make SOCKS5 the conduit for a profile, so compose up / deploy --server use it:
cornus config set-context demo --conduit-mode socks5
# Pin the shared proxy's bind address and suffix in one value:
cornus config set-context demo --conduit-mode 'socks5://.shared:1085?suffix=.demo.internal'

# Per-run override (flag > CORNUS_CONDUIT > profile > default port-forward):
cornus compose up --conduit socks5                    # join the shared proxy
cornus compose up --conduit 'socks5://'               # own proxy, ephemeral port
cornus deploy --server http://cornus.example:5000 --conduit socks5 -f deploy.yaml
```

裸の word (または `socks5://.shared`) はプロファイルの共有プロキシに join します。authority 付きの `socks5://` URL は、それと共存するプライベートな session-local プロキシを起動します。SOCKS5 モードでは、共有 per-server プロキシが `cornus daemon docker` コンテナも cover するため、1 つのプロキシで Docker コンテナと Compose サービスの両方へ名前で到達できます。SOCKS5 CONNECT は TCP-only です。単独の ad-hoc プロキシは [`cornus socks5`](/ja/cli/socks5) です。

### `--via-server`

クラスタープロファイルでは、ログとポート転送は通常 kubeconfig で pod へ直接向かいます。代わりに server-routed パスを強制するには `via-server` を設定します。precedence order は、コマンドごとの `--via-server` フラグ (`--no-via-server` は直接を強制)、次に `CORNUS_VIA_SERVER` (`1`/`0`)、最後にプロファイルフィールドです。これは転送経路だけを変えます。`kube-auth` プロファイルは引き続きクラスタートークンを発行します。

## Kubernetes へのアクセス権から短命資格情報を発行する

Cornus がクラスター内で動き、クラスター自身の OIDC issuer を trust している場合、プロファイルは静的トークンを保存する代わりに、**Kubernetes へのアクセス権から Bearer トークンを発行**できます。CLI は Kubernetes TokenRequest API により短命で audience-scoped な ServiceAccount トークンを要求し、それを資格情報として送ります。Cornus はクラスター JWKS で検証するため、別途用意された Cornus トークンは不要です。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # mints a cluster token AND port-forwards -- no static token
```

サーバー側では、クラスター内 Cornus をクラスターの JWKS に向け、同じ audience を要求します。これは標準 JWKS 検証パスで、サーバー code change は不要です。verifier configuration は [auth and TLS](/ja/topics/auth-and-tls) を、`kube-auth` フィールドは [接続設定](/ja/reference/connection-config) を参照してください。
