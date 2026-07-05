# Cornus とは

Cornus は、`docker compose`、`docker` CLI、Dev Container という Docker の開発ワークフローを、単一の Go バイナリから Kubernetes クラスター (または通常の Docker ホスト) に持ち込むためのソフトウェアです。通常は社内プラットフォームで別々に運用する三つのツールを、自己完結した一つのサービスにまとめます。小規模なチームでも、レジストリ、BuildKit デーモン、GitOps コントローラーを個別に用意することなく、Compose プロジェクトをビルド、プッシュし、実際のクラスターへデプロイできます。

このプロジェクトは単一モジュール (`module cornus`、Go 1.26、Apache-2.0) です。

## 三つのサブシステム

Cornus は、動作に必要なレジストリ、ビルドエンジン、デプロイエンジンを一つのバイナリに収めています。

1. **レジストリ** — 永続的な sha256 コンテンツアドレス指定ストアを基盤とする、小規模な OCI Distribution v1.1 レジストリ (`/v2/*`) です。永続化方式は差し替え可能で、`--storage` によりファイルシステム (既定)、メモリ内、S3 / S3 互換オブジェクトストレージを選べます (`gs://` / `azblob://` は `-tags cloudblob` ビルドで利用できます)。ボリューム / PVC またはオブジェクトバケットを使用するため、再起動後もデータが残ります。[ストレージバックエンド](/ja/reference/storage-backends)を参照してください。
2. **ビルドエンジン** — 別個の `buildkitd` を必要としない、`docker buildx` と同じプロセス内 BuildKit ソルバーです。Dockerfile ビルド、キャッシュマウント (`RUN --mount=type=cache`)、シークレットマウント (`RUN --mount=type=secret`)、SSH エージェント転送 (`RUN --mount=type=ssh`)、名前付きビルドコンテキスト / バインドマウント、リモートキャッシュをそのまま利用できます。ビルドはローカルまたはリモートの Cornus サーバーで実行でき、呼び出し元のディレクトリ、シークレット、SSH エージェントは 9P-over-WebSocket でストリーミングされます。必要に応じて遅延転送もできるため、ビルドが実際に読むバイトだけがネットワークを通過します。[`cornus build`](/ja/cli/build) CLI と `/.cornus/v1/build` HTTP エンドポイントから利用できます。
3. **デプロイエンジン** — 命令的で差し替え可能なデプロイバックエンドです。`dockerhost` (既定) は Docker ホスト上でコンテナを実行し、`containerd` は素の containerd ホスト上でネイティブに実行します (CNI ブリッジネットワークを使用し、dockerd は不要です)。`bare` はデーモンなしで OCI ランタイムを直接駆動します。`kubernetes` (client-go) はデプロイメントとサービスをクラスターへデプロイします。v1 に git の監視や継続的リコンシリエーションはありません。中核機能に加え、リモートワークロードへ 9P で送るクライアントローカルバインドマウント、公開ポートのクライアント側自動転送、ホスト型トンネルを介したワークロードの公開、リモートワークロードの通信を呼び出し元ネットワーク経由にするクライアント側エグレスも提供します。[デプロイバックエンド](/ja/reference/deploy-backends)を参照してください。

サブシステムは共有する Go ストレージではなく OCI HTTP を介して連携します。ビルドエンジンはイメージ参照をレジストリへプッシュし、対象ランタイムがそれをプルします。レジストリのコンテンツストアは `pkg/storage` の背後にあるプライベートな永続化層なので、Cornus は外部 OCI レジストリも使用できます。

## ビルド → プッシュ → デプロイの流れ

ワークロードがクラスターに到達するまでには、サブシステムに直接対応する三つの段階があります。

1. ビルドエンジンでイメージを**ビルド**します。
2. Cornus 自身のもの、または外部のレジストリへイメージを**プッシュ**します。
3. デプロイバックエンドに仕様を適用して**デプロイ**します。バックエンドがイメージをプルして実行します。

[`cornus compose up`](/ja/cli/compose)はこれらの基本操作をまとめた糖衣構文です。明示的に制御したい場合は、[`cornus build`](/ja/cli/build)、[`cornus push`](/ja/cli/push)、[`cornus deploy`](/ja/cli/deploy)を直接実行することもできます。詳しい手順は[クイックスタート](/ja/introduction/quick-start)を参照してください。

## デプロイメントモデル

Cornus はコンテナイメージ (およびビルド済みの静的 CLI バイナリ) として配布され、ローカル Docker コンテナとしても、Kubernetes の第一級サービスとしても動作します (StatefulSet + PVC + Service + RBAC。Helm チャートも提供されます)。ビルド済みマルチアーキテクチャイメージは `ghcr.io/moriyoshi/cornus` に公開されます (semver タグと `edge`)。イメージにはサードパーティーライセンスの帰属表示が含まれます。リリースには `SHA256SUMS` マニフェスト付きの静的 CLI バイナリ (Linux / macOS / Windows) も添付され、Helm チャートは OCI アーティファクトとして公開され、すべてキーレス cosign で署名されます。

レジストリとデプロイのサブシステムに特別な権限は不要ですが、ビルドエンジンには root、またはルートレスユーザー名前空間のスタックが必要です。バイナリの入手方法は[インストール](/ja/introduction/installation)、権限の考え方は[アーキテクチャ概要](/ja/architecture/)を参照してください。

## インターフェイス

* **HTTP:** `/v2/*` (レジストリ)、`/.cornus/v1/build` + `/.cornus/v1/build/attach`、`/.cornus/v1/deploy[/{name}[/{action}]]` + `/.cornus/v1/deploy/attach`、`/.cornus/v1/caretaker/attach` (Pod サイドカーとの接続確立)、`/.cornus/v1/hub/catalog`、`/.cornus/v1/gc`、`/healthz`、`/readyz`、および任意で有効化する Prometheus `/metrics`。
* **CLI (kong):** [`serve`](/ja/cli/serve)、[`config`](/ja/cli/config)、[`build`](/ja/cli/build)、[`push`](/ja/cli/push)、[`deploy`](/ja/cli/deploy)、[`exec`](/ja/cli/exec)、[`port-forward`](/ja/cli/port-forward)、[`tunnel`](/ja/cli/tunnel)、[`socks5`](/ja/cli/socks5)、[`compose`](/ja/cli/compose)、[`daemon`](/ja/cli/daemon)、[`hub`](/ja/cli/hub)、[`token`](/ja/cli/token)、[`health`](/ja/cli/version-health)、[`version`](/ja/cli/version-health)。[`cornus config`](/ja/cli/config)は kubeconfig 風の接続プロファイルを管理します。クラスター内サーバーへの自動ポート転送と、呼び出し元の kube アクセスに基づく短命な資格情報の発行ができるため、手動のトンネルやトークンなしで全コマンドをリモートクラスターに対して実行できます。[リモートクラスターで作業する](/ja/guides/remote-clusters)を参照してください。
* **`cornus compose`:** Docker Compose 互換のコマンドグループ (`up` / `down` / `ps` / `build` / `restart` / `stop` / `start`) です。Compose コマンドを実行中の Cornus サーバーへ振り向けます。ライフサイクルコマンドとワークスペースマウントを含む Dev Container 定義 (`.devcontainer/devcontainer.json`) も、単一コンテナ形式と Compose 形式の両方をネイティブに読み取ります。
* **`cornus daemon`:** 長時間動作するクライアント側ヘルパーデーモンです。`daemon docker` はローカル Docker エンジン API プロキシで、`DOCKER_HOST` を指すと通常の `docker` CLI、`docker compose`、公式の `@devcontainers/cli` までもがリモート Cornus サーバーを操作できます。`daemon mounts` は `cornus compose up -d` がプロジェクトごとに起動するバックグラウンドマウントデーモンです。

## 次に読むページ

* [比較](/ja/introduction/comparison) — Cornus と Skaffold、Tilt、Telepresence、Mutagen、Werf などとの関係。
* [インストール](/ja/introduction/installation) — CLI、コンテナイメージ、またはソースからのビルド。
* [クイックスタート](/ja/introduction/quick-start) — 提供 → ビルド → デプロイの手順。
* [出力モード](/ja/guides/output-modes) — `auto` / `plain` / `fancy` / `json` のレンダリング。
* [アーキテクチャ](/ja/architecture/) — モジュール構成と設計判断。
