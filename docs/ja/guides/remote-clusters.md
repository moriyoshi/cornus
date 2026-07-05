# リモートクラスターで作業する

Cornus はローカルだけでなく、リモートサーバーやクラスター内のデプロイメントに対しても同じように利用できます。ビルドエンジン、レジストリ、デプロイエンジンはすべてサーバー上で動きます。一方でソース、シークレット、バインドマウントは自分のマシンに残り、必要に応じてストリーミングされます。このページでは、リモートの Cornus をローカルのように扱うための要素、つまりエンドポイントのフラグ、接続プロファイル、クラスターへの自動ポート転送、Kubernetes へのアクセス権からの資格情報発行を扱います。

リモートに関する残りの 2 つの話題は隣のページにあります。ビルドコンテキストをリモートのビルダーへストリーミングする方法は[イメージをビルドする](/ja/guides/building-images)、クライアントローカルのディレクトリをリモートのワークロードにバインドマウントする方法は[ワークロードをデプロイする](/ja/guides/deploying-workloads)を参照してください。

クラスター用のプロファイルを対話的に作成するには (クラスター内 Service の自動検出、認証方式の選択、helm の values スニペット生成)、[`cornus setup`](/ja/cli/setup) ウィザードを実行します。

## 仕組み

### CLI をサーバーに向ける

Cornus サーバーと通信するすべてのクライアントコマンドはエンドポイントを受け取り、ベアラートークンを環境から読みます。

| 設定 | 環境変数 | 使用するコマンド |
| --- | --- | --- |
| `--server` | `CORNUS_SERVER` | `deploy`, `exec`, `port-forward`, `socks5`, `tunnel`, `compose`, `hub`, ... |
| `--builder` | `CORNUS_BUILDER` | `build` (リモートビルドの attach エンドポイント) |
| `CORNUS_TOKEN` | `CORNUS_TOKEN` | `/.cornus/v1/*` のベアラー認証、アーカイブの `PUT`、WebSocket の attach |

コマンド上の明示的なエンドポイントが優先されます。なければ選択中の接続プロファイルから解決されます (後述)。エンドポイントには `http(s)://` または `ws(s)://` 形式を指定できます。

呼び出すたびにエンドポイントとトークンを入力し直すのはすぐに煩わしくなります。接続プロファイルを使えば、これらをコマンドラインから完全に取り除けます。

### 接続プロファイル

`cornus config` は kubeconfig 風のファイルを管理します (既定はプラットフォームのユーザー設定ディレクトリ、`--config-file` / `CORNUS_CONFIG` で上書き)。名前付き接続を一度保存しておけば、すべてのコマンドがコマンドラインに何も指定せずそれを使います。

プロファイルは `deploy`、`exec`、`port-forward`、`socks5`、`tunnel`、`compose`、`daemon docker`、`build`、`hub` で尊重されます。エンドポイントの優先順位は明示的なフラグ、次に選択中のコンテキストのサーバーです。トークンの優先順位は `CORNUS_TOKEN`、次にプロファイルのトークンです。コマンドごとにプロファイルを選ぶには `--context <name>` (環境変数 `CORNUS_CONTEXT`) を使います。コンテキスト全体は JSON/YAML ファイルから読み込めます (`--from-file` は基底のレイヤー、`--from-file-override` はファイル側を優先させます)。往復できるようエクスポートすることもできます (`config view --export`)。フィールドの一覧は[接続設定](/ja/reference/connection-config)に記載しています。コマンド自体は [`cornus config`](/ja/cli/config) です。

**関連項目:** [接続設定](/ja/reference/connection-config)、[cornus config](/ja/cli/config)

## 1 回限りのコマンドでリモートサーバーを指定する

プロファイルを作成せずに、単一のコマンドの接続先サーバーを指定します。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_SERVER=https://cornus.example.com CORNUS_TOKEN="$TOKEN" cornus exec -it web -- sh
```

- `--server` は `CORNUS_SERVER` より優先し、`CORNUS_SERVER` は選択中プロファイルより優先します。エンドポイントは `http(s)://` または `ws(s)://` を受け付けます。
- ベアラートークンは `CORNUS_TOKEN` (またはプロファイル) から読み取られます。コマンドフラグとして指定することはできません。

**関連項目:** [cornus deploy](/ja/cli/deploy)

## リモートサーバー用の接続プロファイルを作成する

サーバー URL、トークン、TLS 関連情報を一度保存すれば、コマンドラインでの指定は不要です。

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod
cornus deploy -f app.yaml
```

- `set-context` は既定で名前付きコンテキストを置き換えます。`--merge` を渡すと、未指定のフィールドを維持したまま直接編集します。
- 設定の重ね合わせ順は、`--from-file` (基底)、フラグ、`--from-file-override` (最上位) です。

**関連項目:** [cornus config](/ja/cli/config)、[接続設定](/ja/reference/connection-config)

## プロファイル経由でクラスター内サーバーへ自動ポート転送する

イングレスのないクラスター内 Cornus では、プロファイルに URL ではなく **Service** を指定できます。すると CLI が各コマンドの実行中、自分でポート転送を開きます。組み込みの `kubectl port-forward` 相当です。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster
cornus compose ps     # svc/cornus へ透過的にポート転送して接続する
```

- `--server` は未設定のままにします。空の `server` に `port-forward` ブロックがあれば、クラスター内の Service へ接続します。バックグラウンドの `kubectl port-forward svc/cornus 5000:5000 &` は不要です。転送は各コマンドの前後で設定と削除が行われます。
- `--pf-kube-context` は kubeconfig のコンテキストを選択します。`--pf-service` を設定すると Service の自動検出を省略します。

デプロイした**ワークロード**のポートに到達することは、別の自動的な仕組みです。セッションが公開する `ports:` はすべて `127.0.0.1:<host>` へトンネルされ、[`cornus port-forward`](/ja/cli/port-forward) は未公開のコンテナポートにも必要に応じて到達します。クラスタープロファイルでは、どちらも kubeconfig を使ってワークロードの Pod へ SPDY で直接向かい、必要なら Cornus サーバー経由のトンネルにフォールバックします。

**関連項目:** [cornus config](/ja/cli/config)、[ネットワークと conduit](/ja/guides/networking)

## 自分の Kubernetes へのアクセス権から短命な資格情報を発行する

Cornus がクラスター内で動き、クラスター自身の OIDC issuer を信頼している場合、プロファイルは静的トークンを保存する代わりに、**Kubernetes へのアクセス権からベアラートークンを発行**できます。CLI は Kubernetes TokenRequest API により短命で audience を限定した ServiceAccount トークンを要求し、それを資格情報として送ります。Cornus はクラスターの JWKS で検証するため、別途用意された Cornus のトークンは不要です。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # クラスタートークンを発行してポート転送する。静的トークンは不要
```

- `--kube-auth-audience` はサーバーの `CORNUS_JWT_AUDIENCE` と一致しなければなりません。
- `--kube-auth-namespace` / `--kube-auth-kube-context` の既定値は `--pf-*` の値です。`--kube-auth-expiration-seconds` の既定値は `3600` です。

サーバー側では、クラスター内の Cornus をクラスターの JWKS に向け、同じ audience を要求します。これは標準の JWKS 検証の経路で、サーバー側のコード変更は不要です。

**関連項目:** [接続設定](/ja/reference/connection-config)、[セキュリティと認証](/ja/guides/security)

## プロファイルを切り替え、表示し、削除する

kubeconfig と同様の形式で接続プロファイルの集合を管理します。

```sh
cornus config get-contexts          # プロファイルを一覧表示する (現在のものには * が付く)
cornus config use-context staging   # staging を既定にする
cornus config current-context       # 現在のコンテキスト名を表示する
cornus config view                  # ファイルを表示する (トークンは伏せ字)
cornus config delete-context old    # プロファイルを削除する
```

- `view --show-tokens` はベアラートークンを表示します。`view --export --context prod` は、`set-context --from-file` で再び読み込める単一の Context オブジェクトを出力します。
- `delete-context` は、削除したコンテキストを指していた場合に current-context の参照を解除します。

**関連項目:** [cornus config](/ja/cli/config)

## プロファイルに既定名前空間を設定する

クラスターの検出と kube-auth の既定値に使う、Cornus のインストール先名前空間を記録します。

```sh
cornus config set-context staging -n cornus-system
```

- `-n`/`--namespace` は、`--pf-service` または `--no-detect` が設定されない限り Service とポートを自動検出します。クラスターに接続せず名前空間だけを保存するには `--no-detect` を追加します。

**関連項目:** [cornus config](/ja/cli/config)、[接続設定](/ja/reference/connection-config)

## クライアントからワークロードへの通信をサーバー経由にする

クラスタープロファイルでは、ログとポート転送は通常 kubeconfig で Pod へ直接向かいます。代わりにサーバー経由の経路を強制するには `via-server` を設定します。

```sh
cornus config set-context cluster --merge --via-server
cornus port-forward --via-server web 8080:80    # コマンド単位の上書き
```

- 優先順位は、コマンドごとの `--via-server` / `--no-via-server` フラグ (`--no-via-server` は直接接続を強制)、`CORNUS_VIA_SERVER` (`1`/`0`)、プロファイルのフィールドです。
- 変更されるのは転送経路だけです。`kube-auth` プロファイルは引き続きクラスタートークンを発行します。

**関連項目:** [cornus port-forward](/ja/cli/port-forward)

## リモートデプロイメントのログを追跡し、コマンドを実行する

解決済みのサーバーまたはプロファイルを通じてワークロードのログをストリームし、その中でコマンドを実行します。

```sh
cornus compose logs --follow --tail 100 web
cornus exec -it web -- sh
```

- クラスタープロファイルでは、ログと exec は kubeconfig を使って Pod へ直接接続し、失敗時にはサーバープロキシへフォールバックします。`--via-server` はサーバー経由の経路を強制します。
- `--` の後ろに置いたすべては、`exec` ではコマンドにそのまま渡されます。stdin が端末でなければ `-t` はプレーンストリームに切り替わります。

**関連項目:** [cornus exec](/ja/cli/exec)、[cornus compose](/ja/cli/compose)
