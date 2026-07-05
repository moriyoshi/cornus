# cornus compose

Compose コマンドを、実行中の cornus サーバーの `/.cornus/v1/*` エンドポイントへ redirect する Docker Compose 互換クライアントです。

## Synopsis

```sh
cornus compose [group flags] <subcommand> [flags]
```

## 説明

`cornus compose` は `docker compose` を mirror します。Compose プロジェクト (または devcontainer definition) を読み込み、cornus サーバーに対してサービスをビルド、デプロイ、manage します。drop-in で使うなら `cornus compose` を `docker-compose` として alias できます。標準の `docker` / `docker compose` を使いたい場合は、代わりに [`cornus daemon docker`](/ja/cli/daemon) 経由で動かします。2 つの CLI が同一ではないところ — cornus が尊重できないフラグや、ここでは別の意味になるフラグ — は [Docker Compose 互換性](#docker-compose-compatibility) を参照してください。

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

### 対話シェルの候補

`x-cornus-shells:` は、サービスのイメージが持つ対話シェルを優先順に並べたリストです。[`cornus web`](/ja/cli/web#ターミナルのシェル探索) のターミナルがこれを読み、自身の候補リストより先にプローブします。したがって、珍しいシェルを同梱したイメージのサービスでも、ブラウザー側の設定なしにそのシェルで開きます。

```yaml
services:
  api:
    image: myorg/api
    x-cornus-shells:
      - /bin/bash
      - /bin/busybox sh
```

各エントリーは分割済みの引数リストではなくコマンド**文字列**であり、`command:` や `entrypoint:` と同じ方法で分割されます。したがって `/bin/busybox sh` は 1 エントリーです。候補が 1 つだけの場合は素の文字列も受け付けます (`x-cornus-shells: /bin/bash`)。

プロジェクトレベルの `x-cornus-shells:` ブロックは、自分で宣言していないすべてのサービスの既定値になります。サービス側のリストはそれに追加するのではなく丸ごと上書きします。リストが優先順位そのものだからです。

これはデプロイの内容を何も変えません。どのデプロイバックエンドもこれを読まず、バックエンドが実行中のコンテナと突き合わせるスペックにも含まれないため、編集しても再作成は起きません。

### プロバイダーサービス

サービスは、コンテナとしてビルドまたはプルして実行する代わりに、そのライフサイクルを外部のプロバイダープラグインに委譲できます (compose-spec の `provider:`)。そのようなサービスはプラグインの `type` を指定し、プロバイダー固有の `options` を渡します。

```yaml
services:
  database:
    provider:
      type: awesomecloud
      options:
        type: mysql
        version: "8"
  app:
    image: my/app
    depends_on:
      - database
```

- **探索。** `type: awesomecloud` の場合、cornus は `PATH` 上にあれば Docker CLI プラグイン `docker-awesomecloud` を、なければ `awesomecloud` という名前のバイナリを実行します。プラグインはサーバーではなく、`cornus compose` を実行しているマシン上で動作します。
- **ライフサイクル。** `up` 時、cornus はプラグインを `<plugin> compose --project-name=<project> up [--key=value ...] <service>` として呼び出し、各 `options` エントリを `--key=value` フラグとして渡します (リスト値は繰り返しのフラグになります)。`down` 時は同じものを `down` で呼び出します。プラグインは冪等であることが期待されます。
- **環境変数の注入。** プラグインは標準出力に環境変数を報告します (`setenv KEY=VALUE` プロトコル)。各変数は、そのプロバイダーに `depends_on` するサービスに対して、大文字化したプロバイダーサービス名を接頭辞として付けて公開されます。したがって上記の `database` プロバイダーは `app` に `DATABASE_URL`、`DATABASE_TOKEN` などを提供します。`rawsetenv` 変数は接頭辞なしで依存サービスに公開されます。名前が衝突した場合は依存サービス自身の `environment:` が優先されます。
- **ライフサイクルコマンド。** `cornus compose stop` はプラグインの `stop` を、`start` は `up` を再実行し (冪等)、`restart` は stop してから up します。`down` はプラグインの `down` でリソースを破棄します。
- **制約。** `provider` は `image`、`build`、`deploy` と排他的です。プロバイダーサービスは、デプロイされたワークロードではなく `cornus compose ps` で `provider:<type>` として表示されます。`--watch` によるリロードはプラグインの `up` を再実行し (冪等)、編集したプロバイダー設定を反映します。

## cornus compose up

サービスを作成して開始します (必要ならビルドしてからデプロイ)。

```sh
cornus compose up [flags] [services...]
```

サービスは依存関係 order で起動され、`depends_on` condition を尊重します。明示的なサービスリストを指定した場合、それらのサービスが依存するもの — `depends_on` の推移的閉包 — も `docker compose up web` と同じように起動され、それによって何かが追加されたときは必ずその旨を表示します。

```
also starting dependencies of [web]: [cache db] (--no-deps to skip)
```

取り込まれるのはプロジェクトの有効な選択に含まれるサービスだけなので、`--profile` / `COMPOSE_PROFILES` によって除外された依存は復活せず、除外されたままです。`--no-deps` は指定したサービスだけを起動し、それ以外は起動しません。

フォアグラウンドの `up` は `docker compose up` を mirror します。クライアントローカルバインドマウント (9P でストリーム)、自動転送済み公開済みポート、サービスログへの attach を保持し、`Ctrl-C` まで動き続けます。その後、自分が起動したものを削除します。`-d` / `--detach` はマウント、forwarded ポート、任意の SOCKS5 プロキシ、中継型エグレスセッションをバックグラウンドエージェントに渡して即座に戻ります (後で `down` により停止します)。

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--build` | — | `false` | 開始前にイメージをビルドします (ビルドサービスは常にビルドされます)。 |
| `--ssh` | — | — | ビルド用の SSH エージェント転送: `default` または `id[=socket]` (`RUN --mount=type=ssh`)。繰り返し指定可能。各サービスの `build.ssh` に統合します。 |
| `-d`, `--detach` | — | `false` | Detached モード: デプロイし、クライアントローカルマウント、forwarded ポート、SOCKS5、中継型エグレスをバックグラウンドエージェントに渡して、即座に戻ります。 |
| `--watch` | — | `false` | 読み込んだ compose ファイルと env ファイルの編集を監視し、設定を自動的にリロードして実行中のサービスを再度収束させます。フォアグラウンドと、`-d` 使用時はバックグラウンドエージェントで動作します。下記の [自動リロード](#auto-reload) を参照してください。 |
| `--no-forward-ports` | — | `false` | 公開済みサービスポートをローカルリスナーへ自動転送しません。 |
| `--no-attach` | — | `false` | フォアグラウンドでサービスログをストリームしません (mount/forward は `Ctrl-C` まで保持します)。`--no-attach` が attach しないサービス名を取る `docker compose` とは異なり、これはプロジェクト全体のスイッチです — [Docker Compose 互換性](#docker-compose-compatibility) を参照してください。 |
| `--no-deps` | — | `false` | 指定したサービスの `depends_on` 依存を一緒に起動しません。明示的なサービスリストがある場合にだけ意味があります。リストがなければすでにすべてのサービスが選択されています。 |
| `--force-recreate` | — | `false` | ワークロードに何の変更がなくても再作成します。dockerhost と kubernetes は変更のないワークロードをそのまま残すため、置き換えを強制する手段がこれです。containerd、bare、incus はいずれにせよ `up` のたびに再作成します。 |
| `-t`, `--timeout` | — | — | `docker compose` 互換のために受け付けますが、**尊重されません**。警告して続行します。代わりにサービスへ `stop_grace_period:` を設定してください — [Docker Compose 互換性](#docker-compose-compatibility) を参照してください。 |
| `--no-log-prefix` | — | `false` | ストリームされるログ行にサービス名の接頭辞を付けません。 |
| `--remove-orphans` | — | `false` | Compose ファイルにもう定義されていないサービスのワークロード (サービスの削除やリネーム後に残ったもの) を削除します。指定しない場合、`up` はそれらについて警告するだけです。 |
| `--conduit` | `CORNUS_CONDUIT` | プロファイル | セッション conduit モード: `port-forward` (ポートごとのローカルリスナー、既定) または `socks5` (サービスに名前で到達する 1 つのスプリットトンネルプロキシ)。裸の word はモードだけを設定します。`socks5://host:port[?suffix=SUFFIX]` URL はバインドアドレスと接尾辞も上書きします。`--no-forward-ports` は conduit 全体を無効化します。 |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | プロファイル | サービスの ingress (`x-cornus-ingress`) に SOCKS5 conduit 経由で到達します: `native` (実際のクラスター ingress controller へトンネル) または `emulate` (生成された証明書を使うクライアント側リバースプロキシ)、あるいは `off`。`--conduit socks5` が必要です。`CORNUS_INGRESS_CONDUIT` とプロファイルより優先されます。[イングレス](/ja/guides/ingress) を参照してください。 |
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

### 自動リロード {#auto-reload}

`--watch` を指定すると、`up` はプロジェクトが読み込んだすべてのファイル — compose ファイル、隣接する `.env` または `--env-file` エントリ、各サービスの `env_file:`、そして `include:` / `extends` のターゲット — を監視し続けます。いずれかを編集して保存すると、設定が完全にリロードされ、実行中のプロジェクトが新しい望ましい状態へ再度収束されます。スペックが変わったサービスは再作成され、追加したサービスは開始され、取り除いたサービスは削除されます。変更のないサービスはそのまま実行され続けます。

- **フォアグラウンド** (`up --watch`): 対話セッションはその場でリロードし、新しいセットを保持し続けます (そしてログに再 attach します)。削除されたサービスは — マウント型でも fire-and-forget でも — サーバー側で削除され、フォアグラウンド終了時のクリーンアップと一致します。
- **Detached** (`up -d --watch`): バックグラウンドエージェントがファイルを監視し、変更時に同じ `up -d --watch` を再実行して再プランおよび収束を行います。取り除かれた*エージェント保持*サービス (クライアントローカルマウント、forwarded ポート、中継エグレス) は削除されます。取り除かれた純粋な fire-and-forget サービスはそのまま実行され続けます (通常の再 `up -d` でも残ります — `down` または `up --remove-orphans` で消してください)。ファイル内のサーバーまたは conduit 設定の変更には `down` + `up` が必要です。

完全な `down` は watcher を停止します。部分的な `down SERVICE` は watcher を実行したままにします。

エグレスルーティング model は [エグレス](/ja/guides/egress) を参照してください。

## cornus compose down

サービスを reverse 依存関係 order で停止し、削除します。

```sh
cornus compose down [flags] [services...]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--wait` / `--no-wait` | — | `true` | ワークロードが終了するまで待ってから戻ります。`--no-wait` は delete が受理されるとすぐ戻ります。 |
| `-v`, `--volumes` | — | `false` | Compose ファイルで宣言された名前付きボリュームも削除します (project-scoped、non-external)。外部ボリュームは削除されません。 |
| `--remove-orphans` | — | `false` | Compose ファイルにもう定義されていないサービスのワークロード (サービスの削除やリネーム後に残ったもの) も削除します。 |
| `--rmi` | — | — | `docker compose` 互換のために受け付けますが、**尊重されません** (`local`\|`all`)。警告した上で、ワークロードの削除はそのまま実行します。それ以外の値はそのまま拒否されます。[Docker Compose 互換性](#docker-compose-compatibility) を参照してください。 |
| `-t`, `--timeout` | — | — | `docker compose` 互換のために受け付けますが、**尊重されません**。警告して続行します。代わりにサービスへ `stop_grace_period:` を設定してください。 |

orphan の検出はワークロードの lineage で行われます。すべての `compose up` は各ワークロードに所有プロジェクトを刻印するため、`up` / `down` はプロジェクトの残存ワークロード (削除またはリネームしたサービス) を他プロジェクトのワークロードと区別できます。`up` はそれらについて警告します。(`up` でも `down` でも) `--remove-orphans` はそれらを削除します。記録されたプロジェクトを持たないワークロード — 生の `cornus deploy` や別プロジェクト由来のもの — は決して触れられません。

## cornus compose ps

サービスとその状態を一覧します。

```sh
cornus compose ps [flags] [services...]
```

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `-q`, `--quiet` | — | `false` | 作成されたサービスの resource identifier だけを 1 行ずつ出力します。 |
| `--services` | — | `false` | サービス名だけを依存関係順に 1 行ずつ出力します。 |
| `--format` | — | `table` | 出力形式: `table` (SERVICE / NAME / IMAGE / STATUS) または `json`。 |

既定の列は `SERVICE`、`NAME`、`IMAGE`、`STATUS` であり、`docker compose ps` の `NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS` とは意図的に異なります。docker の列のうち 3 つはローカルコンテナを説明するもので、cornus のデプロイメントには対応物がありません。`DeployStatus` はコマンドも作成時刻もポートバインディングも持ちません。デプロイメントとは、その概念自体を持たないかもしれないバックエンドへ適用されるスペックだからです (kubernetes ではポートは Service に、作成時刻は ReplicaSet に属します)。cornus が代わりに出力するのは、先頭の `SERVICE` — 実際に引くときの手がかりになる Compose の identity — と、それが対応するバックエンドリソースとしての `NAME` です。

スクリプトから使う場合は、列の構成ではなく安定性を約束する出力を使ってください: `--format json` (すべてのフィールド、機械可読)、`--quiet` (resource id)、`--services` (サービス名)。

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
| `--index` | — | — | 選択した各サービスのこのレプリカだけをストリームします。`docker compose logs --index` と同じく 1 始まりです。ライブランタイムから読み取ります。`--all-replicas`、`--from=store`、`--match`、`--severity` とは併用できません。サービスのレプリカ数を超えるインデックスは、有効な範囲を示して拒否されます。 |
| `--all-replicas` | — | `false` | スケールしたサービスの最初のインスタンスだけでなく、すべてのインスタンスをストリームします。各行にはレプリカの序数が付きます。 |
| `--from` | — | `auto` | 読み取り元: `auto`、`runtime`、`store`。以下を参照してください。 |
| `--match` | — | — | このテキストを含む行だけを表示します。`--from=store` を暗黙的に指定します。 |
| `--severity` | — | — | `debug`、`info`、`warn`、`error`、`fatal` の指定レベル以上のレコードだけを表示します。`--from=store` を暗黙的に指定します。 |

注: `--follow` に短縮形の `-f` はありません。`compose` グループがすでに `--file` 用に `-f` を使用しているためです — `--follow` と省略せずに書いてください。docker の習慣で貼り付けた `cornus compose logs -f web` は `web` を Compose ファイル名として読むため、そのエラーは単にファイルがないと報告するのではなく、ここでの `-f` の意味を説明します。

コマンドごとの `--no-color` もありません。cornus の [グローバル `--no-color`](/ja/cli/) はすべての subcommand で使えるため、`cornus compose logs --no-color` は docker のコマンドごとのフラグとまったく同じように動作します。

### 記録済みログの読み取り

サーバーを [`--obs`](/ja/guides/observability#組み込みストア) 付きで実行すると、すべてのワークロード出力が記録されるため、ログは生成元のコンテナより長く残ります。`--from` で読み取り元を選択します。

| 値 | 読み取り元 |
| --- | --- |
| `auto` (既定) | ライブランタイム。ランタイムが何も出力せず失敗した場合にだけストアへフォールバックするため、`runtime` より少ない行を返すことはありません。 |
| `runtime` | 従来と同じ、ライブコンテナの出力のみ。 |
| `store` | `compose down` 後も残る、記録済みの履歴のみ。 |

```sh
# コンテナがなくなった後も回答可能
cornus compose down
cornus compose logs web --from=store --since 1h

# 検索とレベル絞り込みにはストアが必要。ライブのバイトストリームにはレコードがない
cornus compose logs web --match "connection refused"
cornus compose logs web --severity error
```

`--follow` はライブランタイムを追跡するため、`--from=store` とは併用できません。`--match` / `--severity` は `--from=runtime` と併用できません。どちらの組み合わせも暗黙的に解決せず、理由を示して拒否します。

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
| `--pull` | — | `false` | 各ベースイメージの新しいバージョンを常にプルしようとします。各サービスの `build.pull` と OR されるため、プルを要求していないビルドで有効にすることはできても、要求しているビルドで無効にすることはできません。 |
| `--push` | — | `false` | すでに既定の動作です。`cornus compose build` は常に cornus レジストリへプッシュします。このフラグはイメージの送り先を表示するだけです — [Docker Compose 互換性](#docker-compose-compatibility) を参照してください。 |
| `-q`, `--quiet` | — | `false` | ビルドの進捗を表示しません。失敗は引き続き完全に報告されます。 |

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
| `--forward-agent` | — | `false` | ローカルの ssh-agent を exec セッションへ転送します (リモートモードの dockerhost / containerdhost、またはサービスに `x-cornus-agent-forward: true` を設定した kubernetes。[`cornus exec`](/ja/cli/exec) を参照)。 |

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

`restart` と `stop` は `docker compose` 互換のために `-t` / `--timeout` も受け付けます。これは **尊重されません** — 警告して続行します。[Docker Compose 互換性](#docker-compose-compatibility) を参照してください。

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

## Docker Compose 互換性 {#docker-compose-compatibility}

何もしないフラグが黙って受け付けられることはありません。尊重できないフラグは、コマンドが処理を始める前に stderr でその旨を伝え、代わりに何をすべきかを示します。cornus と `docker compose` が単純に同一ではないフラグは、次の 3 つのグループに分かれます。

### 実装済み

| フラグ | 動作 |
| --- | --- |
| `up --no-deps` | 指定したサービスだけを起動し、`up` が既定で行う `depends_on` の展開をスキップします。 |
| `up --force-recreate` | スペックが変わっていなくてもワークロードを置き換えます。仕組みは、`cornus` プロセスの生存期間中は固定されるトークンをラベル 1 つに刻印することです。dockerhost ではこのラベルが、バックエンドが実行中のコンテナと突き合わせるフィンガープリントの一部になるため、スペックが変わっていなければ本来スキップされる再作成を強制できます。kubernetes ではこのラベルが Pod テンプレートの annotation に入るため、Deployment は新しい ReplicaSet をロールします — `kubectl rollout restart` と同じ仕組みです。containerd、bare、incus はいずれにせよ `up` のたびに再作成します。トークンはプロセス単位なので、`up --watch --force-recreate` のリロードはファイル保存のたびに全サービスを再ロールしたりはしません。 |
| `logs --index` | スケールしたサービスのレプリカ 1 つをストリームします。docker と同じく 1 始まりです。 |
| `build --pull` | 各ベースイメージを解決し直します。サービスの `build.pull` と OR されます。 |
| `build -q`/`--quiet` | ビルド進捗の描画だけを抑制します。失敗したビルドはそのエラーを報告します。 |

### 互換性のために受け付けるが尊重しないもの

これらは `docker compose` からコピーしたコマンドラインがそのまま動くように受け付けられ、それぞれ理由と代替を示して一度だけ警告します。

| フラグ | 理由と代替 |
| --- | --- |
| `up`、`down`、`stop`、`restart` の `-t`, `--timeout` | cornus のデプロイ API は呼び出しごとのシャットダウンタイムアウトを持ちません — ライフサイクルのタイミングはサーバーが所有します。猶予期間はサービスの属性です。Compose ファイルに `stop_grace_period:` を設定してください。バックエンドはこれをコンテナの停止タイムアウト / Pod の `terminationGracePeriodSeconds` として適用します。 |
| `down --rmi=local\|all` | このスタックにイメージを削除できるものはありません。デプロイバックエンドが公開するのはワークロードとボリュームだけであり、ビルドされたイメージはサーバー上の cornus レジストリにあります。要求した削除自体は実行されます。サーバーが保持しているものは [`cornus storage`](/ja/cli/storage) で確認し、イメージの領域はバックエンドホスト側で回収してください。`local` でも `all` でもない値は、プロジェクトを読み込む前に拒否されます。 |
| `build --push` | すでに無条件で有効です。デプロイがレジストリからイメージを取得し直すため、compose のビルドは常にプッシュします。黙って飲み込まず注記するのは意味が異なるからです。docker はイメージタグに書かれたレジストリへプッシュしますが、cornus は常に **自身の** レジストリへプッシュし、その注記が実際の送り先ホストを表示します。 |

### 意図的な相違

| 相違 | 詳細 |
| --- | --- |
| `logs` に `-f` の短縮形はない | `compose` グループがすべての subcommand について `-f` / `--file` を所有しており、コマンドごとに上書きできません。`logs --follow` と書いてください。`logs -f web` は、素っ気ない「そのようなファイルはありません」で失敗するのではなく、自分で理由を説明します。 |
| `up --no-attach` は真偽値 | docker ではサービス名を取り、プロジェクト全体を起動しつつそのサービスだけを attach しないままにします。cornus ではプロジェクト全体のスイッチであり、位置引数は起動するサービスを選択します — つまり `up --no-attach web` は `web` だけを起動します。両方を組み合わせると、まさにその点を警告します。 |
| `ps` の列が異なる | docker の列構成ではなく `SERVICE` / `NAME` / `IMAGE` / `STATUS` です。`COMMAND`、`CREATED`、`PORTS` はデプロイメントの status に対応物がなく、kubernetes では意味を持ちません。スクリプトからは `--format json`、`--quiet`、`--services` を使ってください。 |
| `--no-color` はグローバル | cornus はルートコマンドで一度だけ宣言し、すべての subcommand がそれを継承するため、`compose logs --no-color` は docker のコマンドごとのフラグと同じように動作します。 |

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

socks5 モードでは、バックグラウンドエージェントが一つの共有プロキシをホストし、各サービスの短い名前をそこへ登録します。そのためブラウザー (または任意の SOCKS5 クライアント) は、一つのプロキシを通じて `web.cornus.internal`、`api.cornus.internal` などへ到達できます。`cornus web --publish-in-conduit` は同じ共有 conduit に Web UI を公開するため、一つのブラウザープロキシ設定でスタック全体と UI の両方へ到達できます。[一つのブラウザープロキシで Compose スタック全体と Web UI へ到達する](/ja/guides/networking)と [`cornus web`](/ja/cli/web)を参照してください。

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
