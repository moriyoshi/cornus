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
