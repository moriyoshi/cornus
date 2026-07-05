# オブザーバビリティ

Cornus は OpenTelemetry のトレース、メトリクス、ログ、任意の Prometheus スクレイプエンドポイント、liveness/readiness probe を提供します。すべてのテレメトリーは**明示的に有効化する方式で、無効時は余計な負荷がかかりません**。有効にするまで何も導入されず、エクスポーターの goroutine も開始しないため、既定設定で計装済みの呼び出し箇所にかかるコストは実質ありません。

設計 (何を計装し、caretaker との接続確立をまたいで span をどう伝播するか) は[アーキテクチャ概要](/ja/architecture/)を参照してください。以下の変数はすべて[サーバー環境変数](/ja/reference/server-env-vars)リファレンスにあります。

## OpenTelemetry を有効にする

標準 `OTEL_*` 環境変数だけで駆動されるトレース、メトリクス、ログプロバイダーを設定します。Cornus 固有のエクスポーター設定はありません。

```sh
# コレクターを指定して有効にする。任意の OTEL_* 変数で有効になる。
OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317 cornus serve

# SDK の既定値で強制的に有効にする場合:
cornus serve --otel                       # equivalent to CORNUS_OTEL=1
```

- テレメトリーは `CORNUS_OTEL` が真、または標準 `OTEL_*` 変数が設定されている場合だけ設定されます。ただし `OTEL_SDK_DISABLED=true` が優先する場合は決して設定されません。無効時は設定が何もせず、OpenTelemetry API は何もしない既定のままです。
- exporter、sampling、エンドポイントは通常の `OTEL_*` 変数 (`OTEL_EXPORTER_OTLP_*`、`OTEL_TRACES_EXPORTER`、`OTEL_TRACES_SAMPLER` など) で設定します。
- サービス ID はサーバーでは `cornus`、Pod ごとの sidecar では `cornus-caretaker` です。caretaker 接続の span とサーバー側 attach の span は、接続確立の全体を通じた一つのエンドツーエンドトレースを形成します。

## 計装対象

- **HTTP** — `otelhttp` レイヤーがサーバー mux をラップし、要求ごとにサーバー span と標準 HTTP メトリクスを記録します。高 cardinality のパス (digest、デプロイメント名、upload UUID) は経路 template に畳み込むので series が爆発せず、streaming / WebSocket エンドポイントも動作します。
- **ビルドとデプロイ** — ビルド / デプロイ handler は自動 HTTP レイヤーに加え、Cornus 固有の span とメトリクスを追加します。
- **Caretaker** — マウントセッション、プロキシ接続と byte、DNS query を役割ごとに計装します。マウントごとの RX/TX byte は 9P 転送経路境界で計測されます。

## Prometheus でメトリクスをスクレイプする

OTLP プッシュ pipeline と並行してプル型 Prometheus エンドポイントを追加します。active 時だけ auth exempt の `/metrics` 経路を登録し、OpenTelemetry が有効な場合にのみ効果があります。

```sh
CORNUS_METRICS_PROMETHEUS=1 cornus serve --otel
# 次に http://<server>:5000/metrics をスクレイプする
```

## ログ

全プロセスは `log/slog` を通じてログを出します。サーバーと caretaker はその上に OTLP ログエクスポートを重ねるため、テレメトリーが有効なら一つの `slog.Info` がコンソールと OTLP ログパイプラインの両方へ届きます。ログレベルは `CORNUS_LOG_LEVEL` で設定します。

```sh
CORNUS_LOG_LEVEL=debug cornus serve --otel
```

## ワークロードのテレメトリー

ここまでは Cornus 自身の計装です。**あなたのワークロード**のテレメトリーを収集するには、Cornus が Pod ごとの caretaker (ホストバックエンドではコンパニオンコンテナ) の内部に組み込みの **OpenTelemetry Collector** を実行し、アプリを自動的に接続できます。アプリは `127.0.0.1` へ OTLP を送信し、Collector がバッチ処理してバックエンドへエクスポートします。Cornus が `OTEL_*` 環境変数を注入するため、OpenTelemetry SDK 側の設定は不要です。すべてのバックエンド (Kubernetes、dockerhost、containerd、bare) で動作します。

Compose ではサービス単位で有効化します。

```yaml
services:
  web:
    image: web:latest
    x-cornus-telemetry:
      endpoint: otel.example.com:4317   # your OTLP backend (required)
      # protocol: http/protobuf         # default grpc
      # insecure: true                  # plaintext / skip TLS verify
      # signals: [traces, metrics]      # default: all three
      # headers:                        # e.g. an auth token (projected via a
      #   authorization: Bearer <token> #   Secret on Kubernetes, not the pod spec)
```

**プロジェクトレベル**に置けば、1 つのエンドポイントですべてのサービスに対して有効になります (サービス単位のブロックが優先されます)。

```yaml
name: myproj
x-cornus-telemetry:
  endpoint: otel.example.com:4317
services:
  web: { image: web:latest }
  api: { image: api:latest }
```

コマンドラインからは `cornus deploy` と `cornus compose up` で指定できます。

```sh
cornus compose up --telemetry-endpoint otel.example.com:4317
cornus deploy -f app.yaml --telemetry-endpoint https://otel.example.com \
  --telemetry-protocol http/protobuf --telemetry-header "authorization=Bearer $TOKEN"
```

アプリコンテナには `OTEL_EXPORTER_OTLP_ENDPOINT` (ループバックのレシーバーを指します)、`OTEL_EXPORTER_OTLP_PROTOCOL` が自動的に設定され、さらに自分で設定していない場合は `OTEL_SERVICE_NAME` (デプロイメント名) と `OTEL_RESOURCE_ATTRIBUTES` も設定されます。自分で設定した `OTEL_*` はそのまま残されます。

::: tip サイドカーイメージに Collector が必要です
組み込みの Collector はすべてのリリースバイナリと公開イメージにコンパイル済みです。自分でビルドした Cornus では `otelcol` ビルドタグ (`go build -tags otelcol`) が必要で、これがないと caretaker は Collector が組み込まれていないと報告し、ワークロードのスタートアッププローブが失敗します。`cornus version --features` で確認できます。これは Cornus 自身のテレメトリーを制御する上記の `CORNUS_OTEL` とは別のものです。
:::

## 組み込みストア

ここまでの内容はテレメトリーを外部へ送るものであり、すでに Grafana や Datadog、Honeycomb を運用している場合にしか役立ちません。Cornus 自身がその送り先になることもできます。サーバーを `--obs` 付きで起動すると、ワークロードのログ、トレース、メトリクスをローカルのオブザーバビリティデータベースに保持します。

```sh
cornus serve --obs
```

これで 2 つの機能が使えるようになります。片方は一切の設定を必要としないため、分けて説明します。

### 設定不要のログ

ストアを有効にすると、Cornus は管理対象の各ワークロードの標準出力と標準エラー出力を記録します。アプリ側に OpenTelemetry SDK もサイドカーも設定も必要ありません。`compose logs` がすでに表示しているのと同じコンテナ出力を Cornus が読み取り、保持するだけです。

違いは、**コンテナが消えた後も保持し続ける**点です。

```sh
cornus compose up -d
cornus compose down

# コンテナはもう存在しませんが、その出力は残っています。
cornus compose logs web --from=store --since 1h
```

さらに検索もできます。ライブのログストリームは本質的にこれができません。渡されるのはレコードではなくバイト列だからです。

```sh
cornus compose logs web --match "connection refused"
cornus compose logs web --severity error
```

`--match` と `--severity` は `--from=store` を含意します。既定の `--from=auto` はライブのランタイムを読み、ランタイムから何も得られなかったときにだけストアへフォールバックします。したがって、これまでより取得できる行が減ることはありません。

すべてのレプリカが記録され、各レコードはそのインスタンスの序数を保持します。

```sh
cornus observe logs --service web --replica 1   # そのインスタンスだけ
cornus compose logs web --all-replicas          # ライブ、全インスタンス、タグ付き
```

`compose logs` は既定では従来どおり単一インスタンスを表示するので、見慣れた出力は何も変わりません。ファンアウトは `--all-replicas` で明示的に選びます。

### 設定不要のリソース使用量

CPU、メモリ、ネットワーク、ディスクについても同じことが言えます。Cornus は管理下のすべてのワークロードのリソース使用量を一定間隔でサンプリングして記録するので、`docker stats` だけが答えではなくなり、1 時間前にワークロードが何をしていたのかを尋ねられます。

```sh
# 直近 6 時間の、レプリカごとのメモリ
cornus observe metrics 'container_memory_usage' --since 6h

# 実際に見たいのはレートとしての CPU
cornus observe metrics 'rate(container_cpu_time[5m])' --since 6h

# 特定のレプリカだけ
cornus observe metrics 'container_memory_usage{cornus_replica="1"}'
```

すべてのレプリカが個別にサンプリングされ、その序数を `cornus_replica` ラベルとして保持します。したがって、偏りを覆い隠す 1 つの数値を見るのではなく、インスタンス同士を比較できます。

メトリクス名は OpenTelemetry のコンテナーセマンティック規約に従うため、OpenTelemetry で計装された任意のシステム向けに書かれた Grafana ダッシュボードがそのまま動作します。一覧は [`cornus observe metrics`](/ja/cli/observe#cornus-observe-metrics) を参照してください。

サーバーは **自身の** 使用量も同じ方法で、実行しているワークロードの隣に `process_*` として記録します。

```sh
cornus observe metrics 'process_memory_usage'
cornus observe metrics 'go_goroutine_count'
```

問い合わせるより眺めたい場合は、同じサンプルが [`cornus web`](/ja/guides/web-ui#メトリクスダッシュボード) の **Metrics** 画面にグラフとして表示されます。レプリカごとの CPU、メモリ、ネットワーク、ディスク、プロセス数に加えてサーバー自身の使用量も表示され、累積カウンターはレートに微分済みです。

サンプリングは既定で 15 秒ごとに実行されます。間隔を短くすることも、機能全体を止めることもできます。

```sh
cornus serve --obs --obs-metrics-interval 5s
cornus serve --obs --no-obs-record-metrics
```

::: warning Kubernetes で得られる情報は少なくなります
Kubernetes バックエンドでは数値は `metrics.k8s.io` から取得されるため、**metrics-server がインストールされている必要があり**、利用できるのは CPU とメモリだけです。ネットワークカウンター、ディスクカウンター、プロセス数はなく、CPU もホストバックエンドが記録する累積の `container_cpu_time` ではなく瞬間的なレート (`container_cpu_usage`) として届きます。より広い範囲は kubelet の Summary API にありますが、これにはクラスター内のすべての kubelet に到達できる `nodes/proxy` 権限が必要で、メトリクスファミリー 3 つのために払う代償としては見合いません。

メモリの*上限*は通常どおり報告されます。これは測定値ではなく、Cornus 自身が書き込んだ Pod スペックから読み戻しているだけだからです。

欠けているファミリーはゼロとして報告されるのではなく、単に存在しません。「このコンテナは 1 バイトも転送しなかった」と「Cornus には転送したかどうかが見えない」は別の主張だからです。`cornus observe status` はそれらを `metrics.unsupported` として列挙し、Web ダッシュボードは該当するパネルをまったく表示しません。RBAC の権限は `cornus daemon preflight` で確認してください。
:::

### トレースとメトリクスは 1 行で

標準出力はトレースを運べません。トレースとメトリクスについては、アプリのテレメトリーを Cornus に向けます。書くのは **空のブロックだけ** です。

```yaml
services:
  web:
    image: web:latest
    x-cornus-telemetry: {}   # endpoint なし: Cornus 自身へエクスポート
```

Cornus は自身の OTLP レシーバーを endpoint として補完するため、組み込みの Collector はアプリのトレースとメトリクスをログと同じストアへ送ります。両者は `service.name` を共有するので、同一リクエストのログ行とスパンが結び付きます。

`endpoint:` を明示的に設定した場合はこれまでどおり動作し、そちらが優先されます。

::: tip ワークロードから到達できるアドレスが必要です
endpoint の既定値を補完するには `CORNUS_ADVERTISE_URL` (ワークロードからサーバーへ到達できる URL) が必要です。これがない場合、Cornus は警告を出して endpoint を空のままにします。サイドカーの内部で気付かれないまま失敗するエクスポート設定を書き込むことはありません。

実際にはこの要件が問題になることはほとんどありません。テレメトリーは既定で [Cornus への接続経由](#cornus-への接続経由で送られます) で送られるため、送信先を示し caretaker が接続するための URL は必要でも、*ワークロード自身の* ネットワークから到達できる必要はありません。
:::

### 実際のバックエンドへ転送する

Cornus が最終的な保存先である必要はありません。組織の OTLP バックエンドを指定すると、受け取ったものを保存しつつ、そのまま転送します。

```sh
cornus serve --obs \\
  --obs-export-endpoint https://otlp.example.com \\
  --obs-export-header "authorization=Bearer $TOKEN"
```

ワークロードは **Cornus へ 1 回だけ** エクスポートすればよくなります。上流の資格情報も上流への経路も不要です。どちらもサーバー側にあり、各デプロイスペックではなく 1 か所で設定されます。手元には短い保持期間のコピーが残って即座の調査に使え、長期の記録は上流が持ちます。

これはストアの有無にかかわらず動作します。`--obs-export-endpoint` と `--no-obs` を設定すると、Cornus は純粋なテレメトリー **ゲートウェイ** になります。何も保存しないため、`imbh` タグなしのビルドでも利用できます。

`cornus observe status` は転送側の状態を報告し、対処が異なる 2 種類の失敗を区別します。

- **dropped** ... 転送が追いつかず Cornus がレコードを捨てました。上流が遅い状態です。キューは意図的に有界で、詰まったバックエンドが取り込みを止めることは決してありません。
- **failed** ... 上流が拒否した、または到達できませんでした。上流側で何かが壊れています。

### Cornus への接続経由で送られます

Cornus が送信先である場合、テレメトリーはサーバーの OTLP エンドポイントへネットワーク越しに送られるのではありません。Pod の caretaker がすでに保持している接続を通ります。この経路には到達可能な URL も、Pod からの経路も、独自の資格情報も不要で、直接接続が暗黙に依存している NetworkPolicy に壊されることもありません。

**すべてのバックエンドで既定で有効** であり、設定するものは何もありません。上記の空の `x-cornus-telemetry: {}` だけですでにこの経路になります。

再エクスポートと組み合わせると、制限されたクラスターで有用な形になります。**エグレスを一切持たない** ワークロードが既存の Cornus 接続経由でエクスポートし、Cornus が代わりに SaaS バックエンドへ転送します。

エクスポートを通常のトラフィックとして観察したい場合など、直接 HTTP 接続を強制するには次のようにします。

```yaml
x-cornus-telemetry:
  via_mux: false
```

あるいは `--no-telemetry-via-mux` です。明示的な指定は常に既定より優先されます。

ワークロードのネットワークからサーバーへの経路が無い場合に最も価値があり、これは Kubernetes 固有の事情ではありません。[リモート docker ホスト](/ja/guides/remote-docker-hosts) や隔離されたコンテナネットワークでも同様に起こります。

::: info 既定が適用されない場合
次の 2 条件がともに成り立つときにのみ自動的に有効になります。それ以外では、単に不要なのではなく誤った設定になるためです。

- **送信先が Cornus であること。** 明示的な第三者の `endpoint:` を指定した場合、そのバックエンドへ向かう caretaker 接続は存在しないため、Collector が直接接続します。
- **自分で指定していないこと。** `via_mux: false` は尊重されます。

caretaker が接続する URL である `CORNUS_ADVERTISE_URL` が必要です。未設定の場合は、要求していない直接接続へ勝手に切り替わるのではなく、そのメッセージとともにデプロイが失敗します。
:::

### 読み出す

```sh
cornus observe logs --service web --match timeout --since 2h
cornus observe traces --service web --min-duration 500ms
cornus observe trace <trace-id>          # スパンのウォーターフォール
cornus observe metrics 'rate(http_requests_total[5m])'
cornus observe query 'SELECT service, count(*) FROM logs GROUP BY service'
cornus observe status
```

結果が空だったからといって何も起きなかったと結論づける前に、`cornus observe status` を実行してください。負荷によって **破棄された** レコード数を報告します。これは「サービスが静かだった」のか「証拠が捨てられた」のかの違いです。

コマンドの詳細は [cornus observe](/ja/cli/observe) を参照してください。

### Grafana を接続する

クエリ言語がすでに実装されているため、Cornus は Grafana のデータソース API に直接応答します。プロキシやエクスポーターを挟まずに、3 つのデータソースを追加してください。

| データソース | URL |
|---|---|
| Prometheus | `http://<server>:5000/.cornus/v1/obs/prom` |
| Loki | `http://<server>:5000/.cornus/v1/obs/loki` |
| Tempo | `http://<server>:5000/.cornus/v1/obs/tempo` |

各 API のうち、範囲クエリとトレース表示に必要な範囲が提供されます。Cornus がサポートしない構文を使ったクエリは、近似されるのではなく診断メッセージとともに拒否されます。したがってパネルは、正しいデータを表示するか、表示できない理由を伝えるかのどちらかになります。

### 保持期間

| フラグ | 環境変数 | 既定 | 説明 |
|---|---|---|---|
| `--obs` | `CORNUS_OBS` | `false` | ストアを有効にします。 |
| `--obs-dir` | `CORNUS_OBS_DIR` | `<data-dir>/observability` | データベースの配置先。 |
| `--obs-retention` | `CORNUS_OBS_RETENTION` | `168h` (7 日) | これより古いレコードを削除します。日単位に切り上げられます。 |
| `--obs-max-bytes` | `CORNUS_OBS_MAX_BYTES` | `536870912` (512 MiB) | ディスク上のサイズ上限。 |
| `--obs-record-logs` | `CORNUS_OBS_RECORD_LOGS` | `true` | ワークロードの標準出力/標準エラー出力を記録します。`--no-obs-record-logs` で無効化します。 |
| `--obs-record-metrics` | `CORNUS_OBS_RECORD_METRICS` | `true` | ワークロードとサーバーのリソース使用量をサンプリングします。`--no-obs-record-metrics` で無効化します。 |
| `--obs-metrics-interval` | `CORNUS_OBS_METRICS_INTERVAL` | `15s` | 各レプリカをサンプリングする間隔。 |

::: tip すぐに使えます
ストアはすべてのリリースバイナリと公開イメージに含まれており、`--obs` は組み込まれている環境では既定で **on** です。そのため、ダウンロードした `cornus serve` はフラグなしで記録を開始します。次のコマンドで確認できます。

```sh
cornus version --features   # obsstore: yes
```

無効化するには `--no-obs` (または `CORNUS_OBS=0`) を使います。

ストアは cgo 経由で利用する組み込みの Rust データベースなので、自分でビルドした Cornus では明示的に指定しない限りスタブがコンパイルされます。

```sh
eval "$(go run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@v0.3.0 -print-env)"
CGO_ENABLED=1 go build -tags "netgo osusergo otelcol imbh sable_extern_lib" ./cmd/cornus
```

そのようなビルドは `obsstore: no` と報告し、`--obs` は既定で off のままです。明示的に `--obs` を渡した場合は、黙って何も記録するのではなく、ストアが組み込まれていないことをログに出します。
:::

## Health と readiness probe

liveness と readiness エンドポイントは auth 下でも開かれたままなので、probe と load balancer はトークンなしで到達できます。

```sh
# From a script or another host:
curl -fsS http://localhost:5000/healthz
curl -fsS http://localhost:5000/readyz

# In-image healthcheck with no extra tools (Dockerfile):
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```

- `cornus health` は `/healthz` に GET し (5 秒タイムアウト)、サーバーが `200 OK` を返さない限り非ゼロで終了します。イメージに `curl` を必要としないコンテナヘルスチェックです。
- 提供される Kubernetes マニフェストは `/healthz` (liveness) と `/readyz` (readiness) を直接接続します。

**関連項目:** [サーバー環境変数](/ja/reference/server-env-vars) · [cornus serve](/ja/cli/serve) · [cornus health](/ja/cli/version-health) · [インストール](/ja/introduction/installation) · [アーキテクチャ](/ja/architecture/)
