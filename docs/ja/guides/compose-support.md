# Compose の拡張と互換性

Cornus は通常の Compose ファイルを読みます。このガイドが扱うのは、通常でない 2 点です。Cornus が解釈する `x-cornus-*` 拡張フィールドと委譲する compose-spec の `provider:` サービス、そして `docker compose` と意図的に振る舞いが異なる少数のフラグです。フラグごとのリファレンスは [`cornus compose`](/ja/cli/compose)、プロジェクトを動かすための手順は [Compose、Dev Container、Docker CLI](/ja/guides/compose-devcontainers-docker) にあります。

## 仕組み {#how-it-works}

Compose ファイルは `docker compose` と同じ方法で検出、マージ、変数展開され、各サービスが cornus のデプロイメントになります。Compose 仕様が定めるものはすべてその意味のままです。Cornus はその上に 2 つを加えます。

- **`x-cornus-*` 拡張フィールド**。仕様に置き場所のない機能のためのものです。あなたのマシンで発行した資格情報の仲介、コンテナの外向きトラフィックを自分のネットワーク経由にすること、テレメトリーのエクスポートなどです。
- **`docker compose` のフラグがデプロイ API に対応しない場合の独自の答え**。何もしないフラグが黙って受け付けられることはありません。尊重できないフラグは、コマンドが処理を始める前に stderr でその旨を伝え、代わりに何をすべきかを示します。[Docker Compose 互換性](#docker-compose-compatibility)を参照してください。

### 拡張フィールド {#the-extension-fields}

| フィールド | 宣言する内容 | 詳細 |
| --- | --- | --- |
| `x-cornus-shells:` | サービスのイメージが持つ対話シェル (優先順) | [後述](#interactive-shell-candidates) |
| `x-cornus-credentials:` | あなたのマシンで発行しサービスへ仲介する資格情報 | [資格情報](/ja/guides/credentials#compose-ファイルから) |
| `x-cornus-egress:` | 呼び出し元ネットワーク経由の外向きトラフィック | [エグレス](/ja/guides/egress) |
| `x-cornus-ingress:` | サービスに与える公開 HTTP(S) ホスト名 | [イングレス](/ja/guides/ingress) |
| `x-cornus-telemetry:` | サービスのシグナルの OpenTelemetry エクスポート | [オブザーバビリティ](/ja/guides/observability) |
| `x-cornus-agent-forward:` | このサービスへの `exec --forward-agent` の許可 | [`cornus exec`](/ja/cli/exec) |

これらの多くは**プロジェクトレベル**のブロックも取ります。プロジェクトレベルのブロックは、自分で宣言していないすべてのサービスの既定値になり、サービス側のブロックはフィールド単位でマージするのではなく**丸ごと**上書きします。ブロックが独立したキーの集まりではなく一つの選好だからです。これが `x-cornus-shells:`、`x-cornus-credentials:`、`x-cornus-egress:`、`x-cornus-telemetry:` の規則です。

`x-cornus-ingress:` だけは例外で、これは意図的なものです。プロジェクトレベルのブロックはどのサービスのイングレスも**有効にしません**。イングレスはサービスごとのオプトインのままで、プロジェクトのブロックは、すでにオプトインしているサービスに対してドメイン、クラス、TLS issuer をフィールド単位でマージし、サービス自身の値が優先されます。そうでなければ、一つのプロジェクト全体の既定値がスタック内のすべてのサービスを公開してしまいます。`x-cornus-agent-forward:` にはプロジェクトレベルの形式自体がありません。

## 対話シェルの候補 {#interactive-shell-candidates}

`x-cornus-shells:` は、サービスのイメージが持つ対話シェルを優先順に並べたリストです。[`cornus web`](/ja/guides/web-ui#ターミナルのシェル探索) のターミナルがこれを読み、自身の候補リストより先にプローブします。したがって、珍しいシェルを同梱したイメージのサービスでも、ブラウザー側の設定なしにそのシェルで開きます。

```yaml
services:
  api:
    image: myorg/api
    x-cornus-shells:
      - /bin/bash
      - /bin/busybox sh
```

各エントリーは分割済みの引数リストではなくコマンド**文字列**であり、`command:` や `entrypoint:` と同じ方法で分割されます。したがって `/bin/busybox sh` は 1 エントリーです。候補が 1 つだけの場合は素の文字列も受け付けます (`x-cornus-shells: /bin/bash`)。

これはデプロイの内容を何も変えません。どのデプロイバックエンドもこれを読まず、バックエンドが実行中のコンテナと突き合わせるスペックにも含まれないため、編集しても再作成は起きません。

## プロバイダーサービス {#provider-services}

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

## 編集時の自動リロード {#auto-reload-on-edit}

[`up --watch`](/ja/cli/compose#cornus-compose-up) を指定すると、`up` はプロジェクトが読み込んだすべてのファイル — compose ファイル、隣接する `.env` または `--env-file` エントリ、各サービスの `env_file:`、そして `include:` / `extends` のターゲット — を監視し続けます。いずれかを編集して保存すると、設定が完全にリロードされ、実行中のプロジェクトが新しい望ましい状態へ再度収束されます。スペックが変わったサービスは再作成され、追加したサービスは開始され、取り除いたサービスは削除されます。変更のないサービスはそのまま実行され続けます。

```sh
cornus compose up --watch        # フォアグラウンド
cornus compose up -d --watch     # バックグラウンドエージェントが保持
```

- **フォアグラウンド** (`up --watch`): 対話セッションはその場でリロードし、新しいセットを保持し続けます (そしてログに再 attach します)。削除されたサービスは — マウント型でも fire-and-forget でも — サーバー側で削除され、フォアグラウンド終了時のクリーンアップと一致します。
- **Detached** (`up -d --watch`): バックグラウンドエージェントがファイルを監視し、変更時に同じ `up -d --watch` を再実行して再プランおよび収束を行います。取り除かれた*エージェント保持*サービス (クライアントローカルマウント、forwarded ポート、中継エグレス) は削除されます。取り除かれた純粋な fire-and-forget サービスはそのまま実行され続けます (通常の再 `up -d` でも残ります — `down` または `up --remove-orphans` で消してください)。ファイル内のサーバーまたは conduit 設定の変更には `down` + `up` が必要です。

完全な `down` は watcher を停止します。部分的な `down SERVICE` は watcher を実行したままにします。

## Docker Compose 互換性 {#docker-compose-compatibility}

cornus と `docker compose` が単純に同一ではないフラグは、次の 3 つのグループに分かれます。

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

### 意図的な相違 {#deliberate-divergences}

| 相違 | 詳細 |
| --- | --- |
| `logs` に `-f` の短縮形はない | `compose` グループがすべての subcommand について `-f` / `--file` を所有しており、コマンドごとに上書きできません。`logs --follow` と書いてください。`logs -f web` は、素っ気ない「そのようなファイルはありません」で失敗するのではなく、自分で理由を説明します。 |
| `up --no-attach` は真偽値 | docker ではサービス名を取り、プロジェクト全体を起動しつつそのサービスだけを attach しないままにします。cornus ではプロジェクト全体のスイッチであり、位置引数は起動するサービスを選択します — つまり `up --no-attach web` は `web` だけを起動します。両方を組み合わせると、まさにその点を警告します。 |
| `ps` の列が異なる | docker の `NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS` ではなく `SERVICE` / `NAME` / `IMAGE` / `STATUS` です。docker の列のうち 3 つはローカルコンテナを説明するもので、cornus のデプロイメントには対応物がありません。`DeployStatus` はコマンドも作成時刻もポートバインディングも持ちません。デプロイメントとは、その概念自体を持たないかもしれないバックエンドへ適用されるスペックだからです (kubernetes ではポートは Service に、作成時刻は ReplicaSet に属します)。先頭に来るのは `SERVICE` — 実際に引くときの手がかりになる Compose の identity — で、それが対応するバックエンドリソースが `NAME` です。列の構成ではなく安定性を約束する `--format json`、`--quiet`、`--services` を使ってスクリプトを書いてください。 |
| `--no-color` はグローバル | cornus はルートコマンドで一度だけ宣言し、すべての subcommand がそれを継承するため、`compose logs --no-color` は docker のコマンドごとのフラグと同じように動作します。 |

**関連項目:** [`cornus compose`](/ja/cli/compose)、[Compose、Dev Container、Docker CLI](/ja/guides/compose-devcontainers-docker)、[資格情報](/ja/guides/credentials)、[エグレス](/ja/guides/egress)、[デプロイ仕様参照](/ja/reference/deploy-spec)
