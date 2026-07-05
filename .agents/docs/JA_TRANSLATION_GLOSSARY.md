# Japanese Documentation Translation Glossary

Use this table while translating `docs/` into `docs/ja/`. It is an internal
translation aid, not a published documentation page. Keep translated pages
faithful to their English source: do not add explanatory material, glossary
links, or first-use parenthetical English outside the source text.

## Preserve Verbatim

Keep product names, standards, command names, flags, environment variables,
configuration keys, front matter keys, API paths, URLs, code, and values
verbatim. This includes
Cornus, Docker, Kubernetes, BuildKit, Compose, Helm, OCI, HTTP, TLS, JWT,
JWKS, SSH, WebSocket, 9P, CNI, Prometheus, OpenTelemetry, and all text in code
formatting or code blocks.

## Preferred Terms

| English | Japanese |
| --- | --- |
| build / deploy | ビルド / デプロイ |
| deploy spec / pod spec | デプロイスペック / Pod スペック |
| server / client | サーバー / クライアント |
| service / workload | サービス / ワークロード |
| registry / storage | レジストリ / ストレージ |
| backend / engine | バックエンド / エンジン |
| image / container | イメージ / コンテナ |
| cluster / host | クラスター / ホスト |
| remote / local | リモート / ローカル |
| cache / mount | キャッシュ / マウント |
| context / session | コンテキスト / セッション |
| connection profile | 接続プロファイル |
| endpoint / proxy / tunnel | エンドポイント / プロキシ / トンネル |
| secret / credential / token | シークレット / 資格情報 / トークン |
| credential brokering | 資格情報ブローキング |
| authentication / authorization | 認証 / 認可 |
| ingress / egress | イングレス / エグレス |
| reference / source of truth | 参照 / 正本 |
| default / required / optional | 既定 / 必須 / 任意 |
| read-only / full-access | 読み取り専用 / 全権限 |
| filesystem / directory / path | ファイルシステム / ディレクトリ / パス |
| field / value / key / type | フィールド / 値 / 型 |
| request / response / error | 要求 / 応答 / エラー |
| observability / trace / metric | オブザーバビリティ / トレース / メトリクス |
| pluggable / persistence / persistent | 差し替え可能 / 永続化 / 永続的 |
| automatic / manual | 自動 / 手動 |
| explicit / implicit | 明示的 / 暗黙的 |
| external / internal | 外部 / 内部 |
| static / dynamic | 静的 / 動的 |
| named / shared / managed | 名前付き / 共有 / 管理対象 |
| read-only / write-only | 読み取り専用 / 書き込み専用 |
| imperative / declarative | 命令的 / 宣言的 |
| native / embedded | ネイティブ / 組み込み |
| public / private | パブリック / プライベート |
| single / multiple | 単一 / 複数 |
| mint (a token or credential) | 発行する |
| port-forward / port-forwarding | ポート転送 |
| split-tunnel | スプリットトンネル |
| task-oriented recipe | タスク指向のレシピ |
| subsystem | サブシステム |
| environment variable(s) | 環境変数 |
| Kubernetes access | Kubernetes へのアクセス権 |
| rendezvous | 接続確立 / 接続の仲介 (文脈による) |
| clean up / tear down | 後片付けする / 削除する |
| apply / reconcile | 適用する / 収束させる |
| rolling update | ローリング更新 |
| unpublished port | 未公開ポート |
| garbage collection | ガベージコレクション |
| content-addressable store | コンテンツアドレス指定ストア |
| in-memory storage | メモリ内ストレージ |
| anonymous pull | 匿名プル |
| registry advertisement | レジストリの通知 |
| no extra cost when disabled | 無効なら余計な負荷はかかりません |
| dial back | 接続し直す |
| distributed hub store | 分散型ハブストア |
| GC leader gate | GC のリーダー選出による制御 |
| builder (a build-performing peer) | ビルダー |
| privileged / unprivileged | 特権付き / 非特権 |
| snapshot / snapshotter | スナップショット / snapshotter |
| base image / throwaway image | ベースイメージ / 使い捨てのイメージ |
| content hash | コンテンツハッシュ |
| delegate (a build) | 委譲する |
| relay / splice (a connection) | 中継する |
| user namespace | user 名前空間 |
| provider / provider service | プロバイダー / プロバイダーサービス |
| provider plugin / plugin | プロバイダープラグイン / プラグイン |
| lifecycle | ライフサイクル |
| idempotent | 冪等 |
| dependent service | 依存サービス |
| discovery (plugin/binary lookup) | 探索 |
| observability store | オブザーバビリティストア |
| built-in store | 組み込みストア |
| span / waterfall | スパン / ウォーターフォール |
| retention | 保持期間 |
| shed / drop (records under load) | 捨てる / 破棄する |
| resource usage | リソース使用量 |
| sample / sampling (a metric) | サンプリングする / サンプリング |
| reading (one sampled value) | 読み取り |
| semantic conventions | セマンティック規約 |
| metric family | メトリクスファミリー |
| cumulative / instantaneous | 累積 / 瞬間的な |
| label (a metric dimension) | ラベル |
| replica ordinal | レプリカの序数 |
| datasource (Grafana) | データソース |
| full-text search | 全文検索 |
| record (a telemetry row) | レコード |
| log tail / live stream | ログの tail / ライブストリーム |
| survive (outlive the container) | (コンテナより) 長く残る |
| ingress tunnel | イングレストンネル |
| front door (server-side ingress) | フロントドア |
| ingress controller | イングレスコントローラー |
| host mode / alias / passthrough / rewrite | Host モード / alias / passthrough / rewrite (モード名は原文のまま) |
| raw byte stream / raw splice | raw バイトストリーム / 中継 |
| end-to-end TLS | エンドツーエンド TLS |
| default backend (of an ingress controller) | 既定バックエンド |
| prefix | 接頭辞 |
| auto-reload | 自動リロード |
| sidecar / companion / remote companion | sidecar / companion / remote companion (原文のまま) |
| opt into | オプトインする |
| reroute | 再ルートする |
| co-located (server and daemon) | 同じホストにある |
| mount propagation / `rshared` / `rslave` | propagation (原文のまま。マウントオプション名も原文のまま) |
| pinned (network namespace) | pin された |
| always-on / per-instance | 常時オン / インスタンスごと |
| agent forwarding (ssh-agent) | agent 転送 |
| fast path | 高速パス |
| flight record / recorder | フライトレコード / レコーダー |
| activity / unfinished activity | アクティビティ / 終了していないアクティビティ |
| process lifetime | プロセスのライフタイム |
| incarnation (of a process) | インスタンス (実行単位。instance id と対応させる) |
| clean exit / unclean exit | クリーンな終了 / クリーンでない終了 |
| follow (a stream) / live | 追跡する / ライブ |
| backlog (already-written records) | 履歴 |
| keep-alive | キープアライブ |
| Server-Sent Events / SSE | 原文のまま |
| MCP tool / MCP resource | MCP ツール / MCP リソース |

Translate compound terms as a unit before translating their components: build
engine (ビルドエンジン), deploy engine (デプロイエンジン), build cache
(ビルドキャッシュ), bind mount (バインドマウント), cache mount
(キャッシュマウント), secret mount (シークレットマウント), named context
(名前付きコンテキスト), client-side (クライアント側), client-local
(クライアントローカル), server-side (サーバー側), content store
(コンテンツストア), object store (オブジェクトストア), and data directory
(データディレクトリ). Preserve `cornus <command>`, `kubectl <command>`,
flags, configuration keys, and YAML keys verbatim even when their prose
equivalents appear in this table. Front matter is structured configuration, so
keys such as `layout`, `hero`, `image`, `src`, `actions`, `theme`, `link`, and
`linkText` must never be translated.
