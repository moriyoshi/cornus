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
| peer replica / peer credential | 相手レプリカ / レプリカ間資格情報 |
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
| wizard / scenario / preset | ウィザード / シナリオ / プリセット |
| container host | コンテナホスト |
| daemonless / no daemon | デーモンなし |
| rootless / rootful | ルートレス / ルートフル |
| socket (a unix domain socket) | ソケット |
| socket activation | socket activation (英語のまま) |
| selector (which endpoint to use) | セレクタ |
| container runtime / OCI runtime | コンテナランタイム / OCI ランタイム |
| application container (Incus) | アプリケーションコンテナ |
| prerequisite(s) | 前提条件 |
| preflight (the `daemon preflight` check) | 事前チェック (コマンド名は `cornus daemon preflight` のまま) |
| set up (a server) / already set up | セットアップする / セットアップ済み |
| setup guide / next-steps checklist | セットアップ手順 / 次の手順のチェックリスト |
| artifact (a generated setup file) | 生成物 |
| systemd unit | systemd ユニット |
| probe (the server for facts) | 調べる |

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

## Auth and scope terms (settled 2026-07-29)

Settled while translating the scope-mapping and token-exchange sections of
`guides/security.md`, `guides/remote-clusters.md`, and
`reference/connection-config.md`.

| English | Japanese |
| --- | --- |
| claim (JWT) | クレーム |
| scope | スコープ |
| scope map / scope mapping | スコープマップ / スコープマッピング |
| issuer | 発行者 (`iss` claim itself stays verbatim) |
| subject token | subject トークン |
| token exchange | トークン交換 |
| matcher | マッチャー |
| conjunction (all must match) | 論理積 |
| catch-all rule | キャッチオールルール |
| allowlist | 許可リスト |
| narrow (a scope) | 狭める |
| grant (verb) | 付与する |
| delegation | 委譲 |
| audit trail | 監査証跡 |

**Anchors into Japanese headings are written normally (NFC), like any other
link.** This was NOT true before 2026-07-29: VitePress's bundled slugify strips
only the Latin combining range after an NFKD pass, so Japanese voiced marks
survived into the heading id and a normally-typed anchor was silently dead. The
tree carried 34 hand-decomposed fragments to work around it. That is fixed at the
source — `docs/.vitepress/config.mts` recomposes ids to NFC — so decomposed kana
is now a defect everywhere, with no exception. Run `npm run docs:build` before
`docs:check-anchors`: the checker reads the built `dist/`, so a heading you just
added reports dead until it is built.

## Workspace and tiling terms (settled 2026-08-03)

Settled while translating the `## Workspace` section of `cli/web.md`, added when the
Files and Terminal screens merged into one tiled screen.

| English | Japanese |
| --- | --- |
| workspace (the screen) | ワークスペース |
| pane / tile | ペイン / タイル |
| tiled | タイル型 |
| split (verb / noun) | 分割する / 分割 |
| tab (of a pane stack) | タブ |
| mini map (in the pane chooser) | ミニマップ |
| pin (the pane chooser) | ピン留めする |
| gutter (the pinned chooser's strip) | サイドバー |
| file browser | ファイルブラウザー |
| working directory | 作業ディレクトリ |
| mount list | マウント一覧 |
| command palette | コマンドパレット |
| editor / viewer | エディター / ビューアー |
| open (a file, into a pane) | 開く |
| placement target | 配置ターゲット |

`prefix t` and other key spellings stay verbatim: they are typed, not read. The UI
label itself is translated (**ターミナルで開く**) because the UI is not translated —
a reader matching the Japanese against an English screen needs the English nearby,
which the surrounding sentence supplies through the key.

`Enter` / `Space` / `Esc` are left verbatim for the same reason as `prefix t`: they
name a keycap the reader presses, not a word the reader reads. 開く is the verb for
putting a file into a pane and is also the UI label (**開く**); it is deliberately
the same word as ターミナルで開く, because in the UI they are the same verb.

## Inline code formatting vs the source (settled 2026-07-29)

`audit_markdown_translation.py` compares inline code spans as a multiset and
reports `missing` and `extra` separately. They are not equally meaningful, and
the difference is worth knowing before acting on a warning:

- **`missing` is a strong signal.** The source documents a flag, key, path, or
  value that the translation does not. Every `missing` triaged on 2026-07-29 was
  a real gap: a dropped bullet, an untranslated table row, a command the reader
  is meant to run, or a sentence still describing an older version of the source.
- **`extra` is a weak signal, but not noise.** It caught four real defects in one
  sweep — three of them in the ENGLISH source, which code-formatted a literal in
  one row and wrote it bare in the next (`dockerhost`, `perms`, `grpc`, and
  `0600` / `Ctrl-C` elsewhere). The translation was right and the source was
  inconsistent. When `extra` fires, check the SOURCE first.
- **`extra` is also produced legitimately.** CJK sentences often split where
  English runs on, so a translation may name a literal twice where the source
  names it once. That is faithful, not a defect. Six such cases were verified and
  left alone.

Rule: match the source's formatting decisions where the source is consistent;
where it is not, fix the source rather than propagating the inconsistency into
two more trees.

## Source-freshness tracking (added 2026-07-29)

`docs/.translation-state.json` records the SHA-256 of the English source each
translated page was last synced against. `npm run docs:check-translation-freshness`
(chained into `docs:check`) reports every page whose source has moved since.

Workflow when it fires:

1. Read the page against the current English. `git diff` on the source file is
   usually enough to see what moved.
2. Update the translation where the change is substantive.
3. Record it: `python3 .agents/skills/translate-documents/scripts/translation_state.py
   update --path <page.md>` (omit `--path` to re-record everything).

A mismatch proves the SOURCE changed, not that the translation is wrong — a typo
fix in English does not invalidate a translated page. Recording without looking is
the one use that defeats the mechanism.

Why this exists: on 2026-07-29 the structural audit passed for both locales while
three passages were silently stale — a whole intro sentence in
`reference/deploy-spec.md` still describing an older field set, a missing bullet in
`guides/security.md`, and two absent sections. No structural check can see prose
drift; a digest can. The baseline was seeded in bulk that day, which means it
asserts "not known to have drifted since 2026-07-29", NOT "verified correct
page-by-page". Its value is forward-looking.
