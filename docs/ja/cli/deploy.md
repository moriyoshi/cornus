# cornus deploy

デプロイスペックを、ローカルまたはリモート Cornus サーバーに対して適用 (または削除) します。

## 構文

```sh
cornus deploy -f <spec> [flags]
```

## 説明

`cornus deploy` はデプロイスペック (YAML または JSON) を読み取り、適用します。`--server` なしではこのホストのローカルバックエンドにデプロイし、`--server` を指定するとリモート Cornus サーバーに対してデプロイします。ファイル形式は[デプロイスペック](/ja/reference/deploy-spec)を参照してください。

ローカルバックエンドは `CORNUS_DEPLOY_BACKEND` で選びます。`dockerhost` (既定)、`containerd`、または `bare` です。サーバーだけが扱う `kubernetes` を含むその他の値は、警告とともに `dockerhost` へフォールバックします。[デプロイバックエンド](/ja/reference/deploy-backends)を参照してください。
### Knative Serving descriptor

`-f` は `serving.knative.dev/v1`、Kind `Service` の Knative Serving Service manifest (ksvc) も受け付けます。これは native spec、docker-compose、devcontainer と並ぶ first-class descriptor です。`cornus deploy` は `apiVersion`/`kind` を検出し、image、env、ports、command/args、resources、exec probe と、`minScale`、`maxScale`、`target`、`class`、`metric`、`containerConcurrency`、`timeoutSeconds` を持つデプロイメントへ変換します。

Knative Serving を導入した Kubernetes クラスターでは、native `serving.knative.dev/v1` Service に round-trip され、autoscaler が replica と scale-to-zero を管理し、Route が URL を提供します (deploy status に表示)。通常のクラスターまたは `dockerhost` / `containerd` / `bare` では、ワークロードは通常コンテナとして実行され、autoscaling が実現されない警告が出ます。degrade ではなく失敗させるには `CORNUS_KNATIVE_STRICT=true` を設定します。`cornus restart` は新しい revision を作り、scale-to-zero サービスに `stop`/`start` は使えません。

```bash
cornus deploy -f service.yaml --server wss://cornus.example.com
```

現在は Serving のみ (Eventing なし) と単一の always-latest revision (traffic splitting なし) をサポートします。mount、user network、volume、proxy/DNS/hub role を組み合わせた ksvc は一部だけを適用せず拒否します。

`--server` に対しては、既定でフォアグラウンドの deploy-attach セッションになります。クライアントローカルバインドマウント (`--local-mount` を含む) は 9P でストリーミングされ、`--no-forward-ports` がなければ公開ポートはローカルリスナーへ自動転送され、`Ctrl-C` (または `SIGTERM`) は graceful 削除を要求します。`--detach` では仕様を一度 POST してコマンドが終了し、ワークロードは実行を続けます。あとで `cornus deploy -f <spec> --delete --server <url>` で削除します。detach したデプロイではクライアントローカルマウントとクライアント由来資格情報は拒否され、公開ポートは自動転送ではなくサーバーホストにバインドされます。[リモートワークフロー](/ja/topics/remote-workflows)を参照してください。

`--conduit` フラグは `--server` セッションからワークロードへの到達方法を選びます。ポートごとのローカルリスナー (`port-forward`、既定) またはサービス名へ到達する単一 SOCKS5 スプリットトンネルプロキシ (`socks5`) です。`CORNUS_CONDUIT` 環境変数とプロファイルモードより優先されます。`--no-forward-ports` は conduit 全体を無効にします。

`--conduit socks5` では、`--ingress-conduit` によりデプロイで宣言したイングレスホスト (`ingress:` / `x-cornus-ingress`) にもプロキシ経由で到達できます。`native` は実際のクラスターイングレスコントローラーへトンネルし、`emulate` は生成した証明書を使うクライアント側リバースプロキシを実行します。設定の優先順位はフラグ、`CORNUS_INGRESS_CONDUIT`、プロファイルで、`off` は無効化します。[パブリックイングレス](/ja/topics/ingress)を参照してください。

`--egress-*` フラグはコンテナのエグレスをクライアント側ネットワーク経由にします。[クライアント側エグレス](/ja/topics/egress)を参照してください。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `-f`, `--file` | — | 必須 | デプロイスペックファイル (YAML または JSON)。 |
| `--delete` | — | `false` | 適用ではなく名前付きデプロイメントを削除します (ローカルと `--server` のどちらでも機能)。 |
| `-d`, `--detach` | — | `false` | ステートレスなリモートデプロイです。仕様を `--server` に POST し、状態を表示して終了します。ワークロードはクライアントセッションなしで持続します。クライアントローカルバインドマウントは拒否され、公開ポートは自動転送されません。ローカルデプロイでは何もしません。 |
| `--server` | — | — | リモート cornus サーバー URL (`http(s)://` または `ws(s)://`)。設定時はリモートサーバーに対してデプロイを実行します。 |
| `--local-mount` | — | — | `--server` に 9P で提供するクライアントローカルバインドマウント `SRC:DST[:ro][,cache][,async]`。`cache` は不変かつ読み取り専用、`async` は書き込み可能でキャッシュ整合性を保ち、単一 writer 専用です。繰り返し指定可。 |
| `--no-forward-ports` | — | `false` | `--server` セッション中に公開ポートをローカルリスナーへ自動転送しません (conduit も無効)。 |
| `--conduit` | `CORNUS_CONDUIT` | プロファイルモード | セッション conduit モード: `port-forward` (既定) または `socks5`。素の word はモードのみ設定します。`socks5://host:port[?suffix=SUFFIX]` URL はバインドアドレスと service-host 接尾辞も上書きします (`socks5h://` は同義語)。`CORNUS_CONDUIT` とプロファイルモードより優先します。 |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | プロファイル | SOCKS5 conduit 経由でデプロイのイングレスに到達します: `native` (実際のクラスターイングレスコントローラーへのトンネル)、`emulate` (生成した証明書を使うクライアント側リバースプロキシ)、または `off`。`--conduit socks5` が必要です。[パブリックイングレス](/ja/topics/ingress)を参照してください。 |
| `--via-server`, `--no-via-server` | `CORNUS_VIA_SERVER` | プロファイル | kubeconfig で Pod へ直接接続する代わりに、自動転送済みポートを cornus サーバープロキシ経由にします (クラスタープロファイルのみ)。`--no-via-server` は直接経路を強制します。`CORNUS_VIA_SERVER` とプロファイルを上書きします。 |
| `--egress` | — | — | コンテナエグレスをクライアント側ネットワーク経由にします。`env` (プロキシ vars を伝播)、`proxy` (caretaker 転送プロキシ)、`transparent` (nftables + 中継)。 |
| `--egress-route` | — | — | エグレスルーティング規則 `PATTERN=ROUTE`。経路は `client`、`gateway`、`cluster`、`deny`。最初の一致が採用されます。繰り返し指定可。 |
| `--egress-default` | — | `cluster` | 一致しない宛先のエグレス経路。`cluster` (既定)、`client`、`gateway`、`deny`。 |
| `--egress-pac` | — | — | エグレスルーティングを決める PAC-style JS ファイル (`FindProxyForURL`) のパス。`--egress-route` に優先します。 |
| `--telemetry-endpoint` | — | — | 組み込み Collector を有効にし、ワークロードテレメトリーをこの OTLP endpoint へ export します。 |
| `--telemetry-protocol` | — | `grpc` | exporter protocol: `grpc` または `http/protobuf`。 |
| `--telemetry-header` | — | — | 静的 OTLP export header `KEY=VALUE`。繰り返し指定可。 |
| `--telemetry-insecure` | — | `false` | OTLP endpoint への転送セキュリティを無効にします。 |
| `--telemetry-signal` | — | すべて | pipeline を `traces`、`metrics`、`logs` に制限します。繰り返し指定可。 |
| `--telemetry-service-name` | — | デプロイメント名 | 注入される `OTEL_SERVICE_NAME` を上書きします。 |
| `--telemetry-debug` | — | `false` | 収集したテレメトリーも Collector の stdout に出力します。 |

`CORNUS_DEPLOY_BACKEND` 環境変数はローカルバックエンド (`dockerhost` 既定、`containerd`、または `bare`) を選びます。

## 例

ローカル Docker ホストに仕様を適用します。

```sh
cornus deploy -f app.yaml
```

リモートサーバーにデプロイし、フォアグラウンドに留まります。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

detach してデプロイし、あとで削除します。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --detach
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

ローカルディレクトリをワークロードへストリームし、SOCKS5 経由でサービスに到達します。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --local-mount ./data:/data:ro \
  --conduit socks5
```

ルーティング規則でエグレスをクライアント経由にします。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route 'api.internal=client' \
  --egress-default deny
```

ローカルデプロイメントを削除します。

```sh
cornus deploy -f app.yaml --delete
```

## 関連項目

- [デプロイスペック](/ja/reference/deploy-spec)
- [デプロイ backends](/ja/reference/deploy-backends)
- [リモートワークフロー](/ja/topics/remote-workflows)
- [クライアント側エグレス](/ja/topics/egress)
- [資格情報ブローキング](/ja/topics/credentials)
- [`cornus exec`](/ja/cli/exec)
- [`cornus port-forward`](/ja/cli/port-forward)
