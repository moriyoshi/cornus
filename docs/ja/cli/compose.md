# cornus compose

Compose コマンドを、実行中の cornus サーバーの `/.cornus/v1/*` エンドポイントへ redirect する Docker Compose 互換クライアントです。

## Synopsis

```sh
cornus compose [group flags] <subcommand> [flags]
```

## 説明

`cornus compose` は `docker compose` を mirror します。Compose プロジェクト (または devcontainer definition) を読み込み、cornus サーバーに対してサービスをビルド、デプロイ、manage します。drop-in で使うなら `cornus compose` を `docker-compose` として alias できます。標準の `docker` / `docker compose` を使いたい場合は、代わりに [`cornus daemon docker`](/ja/cli/daemon) 経由で動かします。

プロジェクトソースは Compose ファイルまたは devcontainer です。Compose ファイル discovery は working ディレクトリの `compose.yaml`、`compose.yml`、`docker-compose.yaml`、`docker-compose.yml` を探します。devcontainer は、`--devcontainer` が指定された場合、`-f` argument が `devcontainer.json` を指す場合、または Compose ファイルがなく `.devcontainer/devcontainer.json` (または `.devcontainer.json`) が検出できる場合に (auto-detect で) 使われます。混在 repo では Compose ファイルが常に優先されます。

サーバー接続は `--host` から解決されます。なければ選択中の接続プロファイル、それもなければ `http://localhost:5000` です。ビルドされたイメージのタグとデプロイプル ref は、`--registry` / `CORNUS_REGISTRY` / プロファイルから解決したレジストリ、次にサーバーが通知するホスト (`GET /.cornus/v1/info`)、最後にエンドポイントホストに基づいて bake されます。結果のデプロイメント shape は [デプロイスペックリファレンス](/ja/reference/deploy-spec) を参照してください。

## Group flags

これらのフラグは `compose` group に属し、すべての subcommand に適用されます。

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `-f`, `--file` | — | discovery | Compose ファイル。繰り返し指定可能。既定は working ディレクトリの `compose.yaml` / `docker-compose.yml`。 |
| `--env-file` | — | `.env` | 変数 interpolation に使う Env ファイル。既定の `.env` discovery を置き換えます。繰り返し指定可能。後のファイルが優先されますが、プロセス環境はそれらを引き続き上書きします。 |
| `--profile` | `COMPOSE_PROFILES` | — | 指定したプロファイルのサービスを有効化します (compose `profiles:`)。繰り返し指定可能。`COMPOSE_PROFILES` も尊重します。 |
| `--devcontainer` | — | — | `devcontainer.json` ファイル、または `.devcontainer/devcontainer.json` を探すディレクトリへのパス。Compose-file discovery を上書きします。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | dir name | プロジェクト名 (既定: Compose ファイルのディレクトリ名)。 |
| `-H`, `--host` | `CORNUS_HOST` | `http://localhost:5000` | cornus サーバーエンドポイント。選択中の接続プロファイル、次に既定へフォールバックします。 |
| `--registry` | `CORNUS_REGISTRY` | derived | ビルドイメージのタグとデプロイプル ref に bake するレジストリ `host[:port]`。プロファイルと server-advertised 値を上書きします。空の場合はサーバー、次にエンドポイントホストから導出します。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | プロファイル | (クラスタープロファイルのみ) kubeconfig で pod へ直接接続する代わりに、ログと自動転送済みポートを cornus サーバープロキシ経由にします。`--no-via-server` は直接パスを強制します。 |

### Devcontainer 対応

プロジェクトが devcontainer definition (`.devcontainer/devcontainer.json`) から来ている場合、`cornus compose` はそのライフサイクルコマンドを実行します。`initializeCommand` はコンテナが作成される前にホスト上で実行され、サービスごとの `postCreate` / `postStart` / `postAttach` hook はコンテナの起動に合わせて実行されます。プレーン Compose サービスにはライフサイクル hook はありません。

## cornus compose up

サービスを作成して開始します (必要ならビルドしてからデプロイ)。

```sh
cornus compose up [flags] [services...]
```

サービスは依存関係 order で起動され、`depends_on` condition を尊重します。フォアグラウンドの `up` は `docker compose up` を mirror します。クライアントローカルバインドマウント (9P でストリーム)、自動転送済み公開済みポート、サービスログへの attach を保持し、`Ctrl-C` まで動き続けます。その後、自分が起動したものを削除します。`-d` / `--detach` はマウント、forwarded ポート、任意の SOCKS5 プロキシ、中継型エグレスセッションをバックグラウンドエージェントに渡して即座に戻ります (後で `down` により停止します)。

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--build` | — | `false` | 開始前にイメージをビルドします (ビルドサービスは常にビルドされます)。 |
| `--ssh` | — | — | ビルド用の SSH エージェント転送: `default` または `id[=socket]` (`RUN --mount=type=ssh`)。繰り返し指定可能。各サービスの `build.ssh` に統合します。 |
| `-d`, `--detach` | — | `false` | Detached モード: デプロイし、クライアントローカルマウント、forwarded ポート、SOCKS5、中継型エグレスをバックグラウンドエージェントに渡して、即座に戻ります。 |
| `--no-forward-ports` | — | `false` | 公開済みサービスポートをローカルリスナーへ自動転送しません。 |
| `--no-attach` | — | `false` | フォアグラウンドでサービスログをストリームしません (mount/forward は `Ctrl-C` まで保持します)。 |
| `--no-log-prefix` | — | `false` | ストリームされるログ行にサービス名の接頭辞を付けません。 |
| `--conduit` | `CORNUS_CONDUIT` | プロファイル | セッション conduit モード: `port-forward` (ポートごとのローカルリスナー、既定) または `socks5` (サービスに名前で到達する 1 つのスプリットトンネルプロキシ)。裸の word はモードだけを設定します。`socks5://host:port[?suffix=SUFFIX]` URL はバインドアドレスと接尾辞も上書きします。`--no-forward-ports` は conduit 全体を無効化します。 |
| `--egress` | — | — | コンテナエグレスをクライアント側ネットワーク経由にします: `env` (プロキシ var を伝搬)、`proxy` (caretaker 転送プロキシ)、または `transparent` (nftables + 中継)。 |
| `--egress-route` | — | — | エグレスルーティング規則 `PATTERN=ROUTE` (経路: `client`\|`gateway`\|`cluster`\|`deny`)。最初の match が勝ちます。繰り返し指定可能。 |
| `--egress-default` | — | `cluster` | unmatched 宛先のエグレス経路: `cluster`、`client`、`gateway`、または `deny`。 |
| `--egress-pac` | — | — | エグレスルーティングを決める PAC-style JS ファイル (`FindProxyForURL`) へのパス。`--egress-route` より優先されます。 |
| `--telemetry-endpoint` | — | — | 組み込み Collector を有効にし、選択した各サービスのテレメトリーをこの OTLP endpoint へ export します。 |
| `--telemetry-protocol` | — | `grpc` | exporter protocol: `grpc` または `http/protobuf`。 |
| `--telemetry-header` | — | — | 静的 OTLP export header `KEY=VALUE`。繰り返し指定可。 |
| `--telemetry-insecure` | — | `false` | OTLP endpoint への転送セキュリティを無効にします。 |
| `--telemetry-signal` | — | すべて | pipeline を `traces`、`metrics`、`logs` に制限します。繰り返し指定可。 |
| `--telemetry-service-name` | — | デプロイメント名 | 注入される `OTEL_SERVICE_NAME` を上書きします。 |
| `--telemetry-debug` | — | `false` | 収集したテレメトリーも Collector の stdout に出力します。 |

エグレスルーティング model は [クライアント側エグレス](/ja/topics/egress) を参照してください。

## cornus compose down

サービスを reverse 依存関係 order で停止し、削除します。

```sh
cornus compose down [flags] [services...]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--wait` / `--no-wait` | — | `true` | ワークロードが終了するまで待ってから戻ります。`--no-wait` は delete が受理されるとすぐ戻ります。 |
| `-v`, `--volumes` | — | `false` | Compose ファイルで宣言された名前付きボリュームも削除します (project-scoped、non-external)。外部ボリュームは削除されません。 |

## cornus compose ps

サービスとその状態を一覧します。

```sh
cornus compose ps [flags] [services...]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `-q`, `--quiet` | — | `false` | 作成されたサービスの resource identifier だけを 1 行ずつ出力します。 |
| `--services` | — | `false` | サービス名だけを依存関係順に 1 行ずつ出力します。 |
| `--format` | — | `table` | 出力形式: `table` または `json`。 |

## cornus compose ログ

サービスの出力を表示します。選択された各サービスは並行してストリームされます。

```sh
cornus compose logs [flags] [services...]
```

クラスタープロファイルの場合、ログはまず kubeconfig 資格情報でワークロード pod から直接読み取られます。そのパスを開始できない場合にだけサーバープロキシへフォールバックします。

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--follow` | — | `false` | ログ出力を follow します。 |
| `-n`, `--tail` | — | `all` | ログの末尾から表示する行数。サービスごとに適用されます (`all` はすべて)。 |
| `-t`, `--timestamps` | — | `false` | timestamp を表示します。 |
| `--since` | — | — | timestamp (RFC3339) または relative duration (例: `42m`) 以降のログを表示します。 |
| `--until` | — | — | timestamp (RFC3339) または relative duration より前のログを表示します。kubernetes バックエンドでは対応されません (警告付きで無視)。 |
| `--no-log-prefix` | — | `false` | 各ログ行にサービス名の接頭辞を付けません。 |

注: `--follow` に短縮形の `-f` はありません。`compose` グループがすでに `--file` 用に `-f` を使用しているためです。

## cornus compose ビルド

ビルドセクションを定義しているサービスのイメージを、Cornus のビルドエンジンでビルド (およびプッシュ) します。

```sh
cornus compose build [flags] [services...]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--ssh` | — | — | SSH エージェント転送: `default` または `id[=socket]` (`RUN --mount=type=ssh`)。繰り返し指定可能。各サービスの `build.ssh` に統合します。 |
| `--no-cache` | — | `false` | ビルドキャッシュを使いません。 |
| `--build-arg` | — | — | build-time 変数 `KEY=VALUE` を設定します (繰り返し指定可能)。裸の `KEY` は環境から値を取得します。compose の `build.args` を上書きします。 |

## cornus compose exec

サービスの実行中コンテナ内でコマンドを実行します (`docker compose exec` を mirror)。サービスの最初のインスタンスへ exec します。より大きいレプリカインデックスはアドレス指定できません。

```sh
cornus compose exec [flags] <service> -- <cmd> [args...]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `-d`, `--detach` | — | `false` | Detached モード。cornus の exec バックエンドではまだサポートされていません。 |
| `-e`, `--env` | — | — | 環境変数 `KEY=VALUE` を設定します (繰り返し指定可能)。裸の `KEY` はローカル環境から値を取得します。 |
| `-w`, `--workdir` | — | — | コンテナ内でコマンドを実行する working ディレクトリ。 |
| `-u`, `--user` | — | — | このユーザー (name または `uid[:gid]`) としてコマンドを実行します。 |
| `-T`, `--no-TTY` | — | `false` | pseudo-TTY 割り当てを無効化します (既定では stdin が terminal のとき割り当てられます)。 |
| `--privileged` | — | `false` | コマンドに拡張権限を与えます。 |
| `--index` | — | `1` | サービスに複数のレプリカがある場合のコンテナインスタンスインデックス (最初のインスタンスのみアドレス指定可能)。 |

::: warning Kubernetes での `-e` / `--env` の可視性
Kubernetes の `pods/exec` API には exec ごとの環境変数パラメータがありません。そのためクラスタープロファイルでは、cornus はコマンドを `env KEY=VALUE... <cmd>...` としてラップすることでこれをエミュレートします。`-e` で渡した内容は、そのプロセスが生きている間 pod 内の `ps` / `/proc/<pid>/cmdline` から見えてしまいます。また、pod外においても、その pod への exec 権限を持つ誰からも見えます。dockerhost と containerd バックエンドは exec 環境変数をネイティブに設定するため、この露出はありません。クラスタープロファイルでは `-e` で秘匿情報を渡さないでください。マウントしたファイルや、image / デプロイ時の環境変数を代わりに使ってください。
:::

## cornus compose 再起動 / stop / start

サービスを再起動、stop、または start します。それぞれ任意の positional サービス list を取ります (既定: all)。`stop` は reverse 依存関係 order で動作し、`start` と `restart` は転送 order で動作します。バックグラウンドの `up -d` helper が保持するクライアントローカルマウントを持つサービスは拒否されます。停止するには `down` を使ってください。

```sh
cornus compose restart [services...]
cornus compose stop [services...]
cornus compose start [services...]
```

## cornus compose 設定

Compose model を解析、解決、描画します (cornus が parse/merge した view)。

```sh
cornus compose config [flags]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--services` | — | `false` | サービス名を依存関係順に 1 行ずつ出力します。 |
| `--volumes` | — | `false` | トップレベルのボリューム名を並べ替えて 1 行ずつ出力します。 |
| `--images` | — | `false` | 各サービスイメージを依存関係 order で 1 行ずつ出力します。 |
| `--format` | — | `yaml` | 完全なダンプの出力形式: `yaml` または `json`。 |
| `-q`, `--quiet` | — | `false` | model の検証だけを行い、何も出力しません。 |

## cornus compose version

Compose CLI version を表示します。

```sh
cornus compose version [flags]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--short` | — | `false` | バージョン文字列だけを出力します。 |
| `--format` | — | `pretty` | 出力形式: `pretty` または `json`。 |

## Examples

プロジェクトをフォアグラウンドで起動し、ログをストリームします。

```sh
cornus compose up
```

リモートサーバーに対してビルドし、detached モードで開始します。

```sh
cornus compose --host https://cornus.example.com:5000 up --build -d
```

選択したサービスだけを起動し、SOCKS5 conduit 経由で到達します。

```sh
cornus compose up --conduit socks5 web api
```

1 つのサービスのログの最後 100 行を follow します。

```sh
cornus compose logs --follow --tail 100 web
```

プロジェクトを削除し、名前付きボリュームも削除します。

```sh
cornus compose down --volumes
```

サービスのコンテナでシェルを開きます。

```sh
cornus compose exec web -- sh
```
