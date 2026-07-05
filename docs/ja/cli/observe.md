# cornus observe

サーバーの組み込みオブザーバビリティストアから、**ワークロード** の記録されたテレメトリー (ログ、トレース、メトリクス) を照会します。

## 書式

```sh
cornus observe logs    [flags]
cornus observe traces  [flags]
cornus observe trace   <trace-id> [flags]
cornus observe metrics <promql> [flags]
cornus observe query   <sql> [flags]
cornus observe status  [flags]
```

## 説明

「何が起きたか」に答えるコマンドは 3 つあります。どれがどれなのかを明確にしておきます。

| コマンド | 対象の挙動 | 範囲 |
|---|---|---|
| [`cornus activity`](/ja/cli/activity) | **Cornus 自身** (サーバーと caretaker) | 1 台のサーバーのフライトレコード |
| [`cornus compose logs`](/ja/cli/compose) | あなたのワークロード | 1 プロジェクトのサービスの tail |
| `cornus observe` | あなたのワークロード | ストアが保持するすべて。加えてトレースとメトリクス |

`cornus observe` はサーバーが記録したすべてのワークロードを横断して読み取り、ログの tail が本質的に運べない 2 つのもの、分散トレースとメトリクス系列を提供します。

`--obs` 付きで起動したサーバーが必要です ([オブザーバビリティ](/ja/guides/observability#組み込みストア) を参照)。サーバーがない場合、各サブコマンドは空の結果ではなく対処方法を示すメッセージとともに失敗します。空の結果は「何も起きなかった」と読めてしまい、それは別の、はるかに誤解を招く答えだからです。

## コマンド

### `cornus observe logs`

すべてのワークロードを横断して記録済みのログレコードを検索します。ライブの tail と違い、これらはレコードを生成したコンテナより長く残り、検索できます。

```sh
# 直近 2 時間に checkout は何を出力したか
cornus observe logs --service checkout --since 2h

# どのワークロードであれ、失敗を探す
cornus observe logs --match "connection refused"

# エラーだけ
cornus observe logs --severity error

# 1 リクエストに属するログ行をすべて
cornus observe logs --trace 4bf92f3577b34da6a3ce929d0e0e4736
```

| フラグ | 説明 |
|---|---|
| `--service` | このワークロード (デプロイメント名) のみ。 |
| `--match` | 本文にこのテキストを含むレコードのみ (全文検索)。 |
| `--severity` | `debug`、`info`、`warn`、`error`、`fatal` 以上のレコードのみ。 |
| `--stream` | `stdout` または `stderr` のみ。 |
| `--trace` | このトレース ID に対応するレコードのみ。 |
| `--since` / `--until` | 時刻の範囲。RFC3339、Unix 秒、または `2h` のような期間。 |
| `--limit` | 最大レコード数 (既定 200)。 |
| `--oldest` | 最新ではなく最も古いレコードを返します。 |

レコードは古い順に表示されます。`--oldest` を指定しない限り、`--limit` は **最新の** 一致を保持します。

### `cornus observe traces`

なぜ遅かったのかを調べる前に、*どの* リクエストが遅かった、あるいは失敗したのかを見つけます。

```sh
# 遅いもの
cornus observe traces --service checkout --min-duration 500ms

# 壊れているもの
cornus observe traces --status error --since 1h
```

| フラグ | 説明 |
|---|---|
| `--service` | このワークロードのスパンを含むトレースのみ。 |
| `--name` | この名前のスパンを含むトレースのみ (例: `GET /checkout`)。 |
| `--status` | このスパンステータスのトレースのみ (例: `error`)。 |
| `--kind` | `server`、`client`、`producer`、`consumer`、`internal`。 |
| `--min-duration` / `--max-duration` | 実行時間の範囲 (例: `500ms`)。 |
| `--since` / `--until` | トレース開始時刻の範囲。 |
| `--limit` | 最大トレース数 (既定 50)。 |

### `cornus observe trace`

1 つのトレースをウォーターフォールとして表示し、時間がどこで消費されたか、どのサービスが最初に失敗したかを確認します。

```sh
cornus observe trace 4bf92f3577b34da6a3ce929d0e0e4736
```

```
trace 4bf92f3577b34da6a3ce929d0e0e4736 — 4 spans over 812.4ms

GET /checkout                              web              812.4ms  ████████████████████████████
  authorize                                auth             120.1ms      ████
  charge                                   payments         640.2ms          ██████████████████████  !Error
    POST /v1/charges                       payments         631.8ms           █████████████████████
```

親が記録されなかったスパンもルートとして表示されます。部分的にしか収集されていないトレースこそ、まさに誰かがこれを読んでいる状況なので、スパンを落とすことはありません。

### `cornus observe metrics`

ワークロードがエクスポートしたメトリクスに対して PromQL の範囲クエリを評価します。

```sh
cornus observe metrics 'rate(http_requests_total[5m])' --since 6h --step 1m
```

| フラグ | 既定 | 説明 |
|---|---|---|
| `--since` | `1h` | 範囲の開始。 |
| `--until` | 現在時刻 | 範囲の終了。 |
| `--step` | `1m` | 返される系列の解像度。 |

OpenTelemetry のメトリクス名は Prometheus の表記に対応付けられます。ドットはアンダースコアになるので、`http.server.duration` は `http_server_duration` として照会します。**単位のサフィックスは付きません** — `container_cpu_time_seconds_total` ではなく `container_cpu_time` です。サポートされる PromQL プロファイル外の構文は、近似されるのではなく診断メッセージとともに拒否されます。

#### Cornus が自動的に記録するメトリクス

`--obs` を有効にすると、ワークロードが何もエクスポートしなくてもこれらが存在します。以下の名前は PromQL での表記で、ラベルは照会時の表記で示しています。

| メトリクス | 単位 | ラベル | 意味 |
|---|---|---|---|
| `container_cpu_time` | 秒 | `cornus_replica`, `cpu_mode` | 累積 CPU 時間。`rate()` で使います。Kubernetes では利用できず、代わりに下記のレートが記録されます。 |
| `container_cpu_usage` | コア | `cornus_replica` | 瞬間的な CPU。累積値の取得元がない Kubernetes のみ。 |
| `container_memory_usage` | バイト | `cornus_replica` | 再利用可能なページキャッシュを除いた使用中メモリ。`docker stats` が示すのと同じ値です。 |
| `container_network_io` | バイト | `cornus_replica`, `network_io_direction`, `network_interface_name` | 累積トラフィック。Kubernetes では利用できません。 |
| `container_disk_io` | バイト | `cornus_replica`, `disk_io_direction` | 累積ブロック I/O。Kubernetes と Incus では利用できません。 |
| `cornus_container_memory_limit` | バイト | `cornus_replica` | 設定されている場合の適用中の上限。Kubernetes では metrics-server ではなく Pod スペックから取得します。 |
| `cornus_container_pids` | 個数 | `cornus_replica` | プロセスとスレッド。Kubernetes では利用できません。 |

どのメトリクスもデプロイメント名を `service` として保持します。

使用中のバックエンドで「利用できません」と記されたメトリクスは、0 の系列ではなく**系列そのものが存在しません**。Kubernetes で `container_network_io` が無言なのは、ワークロードがバイトを転送したかどうかを Cornus が観測できないからであり、「1 バイトも転送しなかった」というのとは別の主張です。`cornus observe status` はそれらを `metrics.unsupported` として列挙し、[`cornus web`](/ja/guides/web-ui#メトリクスダッシュボード) のダッシュボードは該当パネルを常に空のまま描くのではなく非表示にします。

サーバー自身の使用量も同じ場所に記録されます。`process_cpu_time`、`process_memory_usage`、`process_memory_virtual`、`process_thread_count`、`process_open_file_descriptor_count`、`process_disk_io` に加えて、Go ランタイムのメトリクス (`go_goroutine_count`、`go_memory_used` など) と Cornus 自身のカウンター (`cornus_builds`、`cornus_deploys`) です。

どちらのカウンターも `outcome` ラベル (`ok` / `error`) を持ち、`cornus_deploys` はさらに `action` を持ちます。`cornus_deploys` が数えるのはデプロイを**変更する**アクションだけです。`apply`、`delete`、`volume-delete`、`start`、`stop`、`restart` です。読み取りのみの要求 (`list`、`status`) はトレースはされますが数えません。これらはクライアントがポーリングするものなので、数えてしまうとデプロイ量ではなく開いているダッシュボードの数を表す値になってしまうからです。

`cornus_builds` は、サーバーが受け付けたビルドを 4 つの経路すべてで数えます。tar アップロード、`cornus build` のセッション、そして[コンテナー化ビルダー](/ja/reference/server-env-vars#delegating-builds-to-a-builder)へ処理を委譲する 2 つの経路です。どちらだったかは `delegated` ラベルが示します。`delegated="false"` の場合、`outcome` はビルド自身の結果です。`delegated="true"` の場合は違います。ビルド結果はサーバーが解析せずに転送するストリームの中を流れるため、この場合の `outcome` は「呼び出し側がビルダーに到達できたかどうか」を表します。`cornus_build_duration` は対応するヒストグラムで、同じ規則に従います。

`cornus_server_network_io` はプロセス単位ではなくネットワーク名前空間単位です。コンテナー内ではサーバーのトラフィックですが、ホストへのインストールではホスト全体のトラフィックになります。セマンティック規約の `process.network.io` ではなく `cornus_` プレフィックスを付けているのは、その名前が約束するプロセス単位の値だと主張しないためです。

::: tip ラベル名はドットではなくアンダースコアです
`cornus.replica` ではなく `cornus_replica` です。Prometheus のデータモデルにはラベル名中のドットを置く場所がなく、ストアの PromQL もそれを表現できないため、Cornus はアンダースコア表記で出力します。ドットを使ったマッチャーは **系列が 0 件になり、エラーも出ません**。これは考えうる限り最も紛らわしい失敗の仕方なので、フィルターが黙って何にも一致しないときは最初にここを確認してください。
:::

::: warning ヒストグラムには SQL が必要です
ヒストグラムのメトリクス (`http.server.request.duration` など) は記録されますが、ストアの PromQL プロファイルでは名前で選択できません。代わりに [`cornus observe query`](#cornus-observe-query) で読み出してください。

```sh
cornus observe query "SELECT metric, count, sum FROM metrics_histogram ORDER BY time DESC LIMIT 10"
```
:::

### `cornus observe query`

型付きコマンドでは扱えない問いのための生の SQL です。

```sh
cornus observe query 'SELECT service, count(*) AS n FROM logs GROUP BY service'
```

テーブル: `logs`、`spans`、`metrics_gauge`、`metrics_sum`、`metrics_histogram`、`metrics_exp_histogram`、`metrics_summary`。`histogram_quantile`、`matches` (全文検索)、`json_get_str` を含む UDF が利用できます。

### `cornus observe status`

ストアが何を保持しているか、また何かを失っていないかを報告します。

```sh
cornus observe status
```

```
directory   /var/lib/cornus/observability
retention   168h0m0s
size cap    512.0 MiB
buffered    50.8 KiB

TABLE                      ROWS   SEGMENTS  OLDEST
logs                        1284          3  2026-07-19T04:11:02Z
spans                        412          1  2026-07-25T22:03:44Z
metrics_gauge               8640          2  2026-07-19T04:11:02Z
metrics_sum                17280          4  2026-07-19T04:11:02Z

metrics     sampling 3 replica(s) every 15s
  recorded  1728 readings

dropped     0
```

検索結果が空だったからといって何も起きなかったと結論づける前に、これを確認してください。`dropped` が 0 でない場合、ストアは負荷によってレコードを捨てているので、証拠が存在しなかったのではなく失われている可能性があります。

`metrics` のブロックは、クエリが何も返さないときに見分けがつかない 3 つの失敗を区別します。`sampling 0 replica(s)` は何もデプロイされていないこと、`FAILED` が 0 でない場合はバックエンドが拒否したこと (`cornus daemon preflight` を確認してください。Kubernetes では通常 `metrics.k8s.io` の権限不足です)、`DROPPED` が 0 でない場合は読み取り自体は行われたが負荷によって捨てられたことを意味します。

`repeated` の行は失敗ではありません。バックエンドが同じ読み取りをそのまま返し直した回数であり、レコーダーはそれを 2 回書き込まずにスキップします。Kubernetes では想定どおりであり、通常は大きな値になります。ソースが metrics-server で、そのスクレイプ間隔 (15-30 秒) はサンプリング間隔より粗いため、複数回のポーリングが同じ読み取りを観測するからです。これは系列が `--obs-metrics-interval` から期待されるより疎になる理由の正直な説明でもあります。解像度はソースが公開する内容によって決まるので、`recorded` が小さいのに `repeated` が大きい場合は、間隔を緩めても何も失われないということです。

## 共通フラグ

各サブコマンドは `--server` (`CORNUS_SERVER`) でサーバーを明示的に指定できます。指定しない場合は選択中の [接続プロファイル](/ja/cli/config) が使われます。

`--output json` はレコードそのものを JSON で出力します。`logs`、`traces`、`trace`、`metrics`、`query` は配列、`status` はオブジェクトなので、結果をそのまま `jq` に渡せます。

## 関連項目

**関連項目:** [オブザーバビリティ](/ja/guides/observability) · [cornus activity](/ja/cli/activity) · [cornus compose](/ja/cli/compose) · [cornus serve](/ja/cli/serve)
