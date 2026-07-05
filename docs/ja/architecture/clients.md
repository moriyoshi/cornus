# Docker 互換クライアントと接続プロファイル

3 つのクライアント機能が同じサーバー API に集約され、1 つの変換経路を共有します。`cornus compose` (docker-compose の代替)、`cornus daemon docker` (標準 `docker` CLI 向けの Docker エンジン API プロキシ)、そしてネイティブの開発コンテナ対応です。これらはすべて、単一の `cornus` バイナリのサブコマンドです。

## Docker API プロキシ

`cornus daemon docker` は Unix ソケット上で Docker エンジン REST API の一部を提供し、コンテナ操作をリモートサーバーに対する Cornus デプロイへ変換します。`DOCKER_HOST` をそのソケットに向けると、標準 `docker` CLI はリモート Cornus 上でワークロードを実行し、呼び出し元のローカルバインドマウントディレクトリは 9P でストリームされます。

Docker の create/start 分割は、**バッファリング** によって Cornus の原子的な適用へ対応付けられます。`docker create` は要求を変換してレコードを保存しますが、サーバーには連絡しません。`docker start` は長寿命の deploy-attach セッションを開き、コンテナの存続期間中ずっと接続を維持します (そのため 9P マウントが動き続けます)。`docker ps` / `inspect` はバッファリングされたレコードから Docker 形式の応答を合成し、create 時のラベルを返すのでラベルフィルターが機能します。

`stop` / `start` は **レコード単位** で往復します。`stop` はセッションを取り消してワークロードを削除しますが、レコードは `exited` として保持するため、`docker ps -a` には引き続き表示されます。`start` はセッションを再び開いて再デプロイします。これは意図的にコンテナ単位の一時停止ではありません。クライアントが提供する 9P マウントは呼び出し元のセッションより長生きできないため、ワークロードは一時停止ではなく再作成されます。Cornus の再作成型デプロイモデルと一貫しています。対象は `run`、`ps`、`inspect`、`stop`、`start`、`rm`、`logs`、`exec`、`attach` (対話的 `-it` を含む)、`stats`、`cp` にわたり、標準の `docker compose up/ps/down` も動作します。`/build` は範囲外です。ビルドは `cornus build` の役割です。

フォアグラウンドの `docker run` が動くのは、プロキシが経路だけでなく dockerd の protocol そのものを再現するからです。セッションが稼働中になるまで attach を待機させ、`wait?condition=next-exit` にはヘッダーを即座に返しつつ body は exit 時にだけ返し、CLI が知っている両方の encoding でライフサイクルイベントを publish します。この忠実さにより、VS Code の開発 Containers extension の中核である公式 `@devcontainers/cli` が、プロキシに対して無変更で動きます。

## Compose クライアントと開発 Containers

`cornus compose` はローカル driver ではなくクライアントです。Compose ファイルを解析し、各サービスを `DeploySpec` と任意のビルド plan に変換し、`depends_on` から依存関係 order を計算して、稼働中のサーバーを操作します。`up` は (`build:` section があれば) ビルドし、レジストリへプッシュし、依存関係 order でデプロイします。`down` はその逆順です。

クライアントが提供するマウントはクライアントより長生きできないため、ローカルバインドマウントを持つサービスには稼働中セッションが必要です。`-d` なしの `up` はそれらのサービスを Ctrl-C まで **フォアグラウンド** で実行します。一方、`up -d` はそれらを unified クライアントエージェント (後述) に渡し、エージェントがバックグラウンドでセッションを保持するためコマンドは戻ります。

**開発 Containers** は、`.devcontainer/devcontainer.json` を Compose パスと同じ compose プロジェクト model へ変換することでネイティブに読み込まれます。そのため、すべての `up` / `down` / `ps` / `build` コマンドが無変更で再利用されます。single-container (`image` / `build.dockerfile`) と compose-based (`dockerComposeFile` + `service`) の両方を対応します。ライフサイクル hook (`onCreate`、`postCreate`、`postStart` など) はサービス ready 後にサーバー側 exec でコンテナ内に実行されます。これは 9P セッションを誰が保持しているかに関係なく、すべての up パスで動きます。

## 宣言的な収束処理と命令的プロキシ

この 2 つの対象範囲は、設計上、宣言的 / 命令的の境界の反対側にあります。compose ファイルは desired-state 説明なので、compose パスは小さな **宣言的収束エンジン** を動かします。呼び出し元がサービスの desired set を適用すると、エンジンは稼働中 resource、つまり 9P マウントセッションとポート転送 / SOCKS5 exposure を一致させます。dimension ごとの fingerprint を持つため、exposure だけの変更で健全なマウントを削除することはありません。

Docker API はもともと命令的です (`create` / `start` / `stop` / `rm` は離散的な edge イベント)。またコンテナは immutable なので、プロキシは収束しません。代わりに per-container 状態 machine が Docker API contract を encode します。両者は、その下のレイヤー、つまり per-workload deploy-attach hold と conduit exposure primitive を共有します。

## unified クライアントエージェント

バックグラウンドでクライアントが保持するすべてのセッションは、user ごとに 1 つの長寿命プロセス、つまり `cornus daemon agent` に存在します。これは単一の control ソケット (`$XDG_RUNTIME_DIR/cornus/agent.sock`) 経由で到達できます。`cornus compose up -d` と `cornus daemon docker` は thin クライアントで、エージェントを ping-to-reuse するか spawn し、ソケット経由で work を登録します。`cornus daemon status` / `stop` はそれを確認し、削除します。

クライアントは接続 ID を事前に解決して work と一緒に送ります。エージェントのプロセス環境は spawn 時点で固定されるためです。エージェントは同じプロファイル logic に基づいて再解決するので、バックグラウンド compose セッションはプロファイルのトークン、TLS、kube-auth を取得できます。同じサーバーを対象にする work は **1 本の接続と 1 つの conduit** を共有します。そのため、単一の SOCKS5 プロキシで docker コンテナと compose サービスの両方へ名前で到達できます。

## ローカル Web UI

[`cornus web`](/ja/cli/web) はクライアント側のブラウザーインターフェースです。BFF は、サーバーのワークロード状態を Compose プロジェクト構造と稼働中のバックグラウンドエージェント一覧に結合します。これらのクライアント所有情報は、サーバーの平坦化された API には存在しません。組み込み SPA は、ワークロードとプロジェクトのビュー、依存関係グラフ、マウント、トンネルと転送、許可対象を限定したファイル編集、ログ、exec を提供します。

UI には認証がないため、ループバック専用です。プロジェクトの適用では `cornus compose ... up -d` を再利用するため、CLI とブラウザーの操作には同じ収束エンジンとバックグラウンドエージェントの存続期間規則が適用されます。

## 接続プロファイルとリモートクラスター

クラスター *内*にある Cornus サーバーへ到達するには、以前は手作業の `kubectl port-forward` と手作業で用意したトークンが必要でした。接続プロファイルはサーバー側の変更なしに、その穴をクライアント側で埋めます。

- **プロファイル** は `cornus config` で管理される kubeconfig 風のコンテキストです。エンドポイント、TLS material、任意のポート転送対象、任意の kube-auth block を持ちます。1 つの resolver が選択されたコンテキストをすべてのクライアントコマンドへ通し、エンドポイントには明示的フラグ > コンテキストサーバー > エンドポイントの自動ポート転送、資格情報には `CORNUS_TOKEN` > kube-auth 発行 > 静的プロファイルトークンという 2 つの precedence chain を適用します。
- **自動ポート転送**: クラスター内サービスを指すプロファイルはコマンド lifetime 中だけ `kubectl port-forward` 相当を開き、クライアントをローカル forwarded アドレスに向けます。`cornus config set-context --namespace <ns>` は設定時に client-facing cornus サービスを検出します。match が 0 件または複数件の場合は、candidate を列挙して hard エラーになります。
- **Kube-auth**: プロファイルは Kubernetes TokenRequest API により、短命で audience scoped な ServiceAccount トークンを発行できます。クラスター内サーバーは既存の JWKS 検証パスでそれを検証するため、開発者の Kubernetes へのアクセス権が Cornus 資格情報としても機能します。サーバーに発行エンドポイントは不要です。

プロファイルの TLS 設定は REST 転送経路とすべての WebSocket 接続の両方に適用されるため、リモートビルドや deploy-attach セッションもカスタム CA や mTLS クライアント cert を尊重します。2 つの資格情報は別物である点に注意してください。kube 資格情報はポート転送の *設定* を認証し、Cornus 資格情報はトンネルの*中*を認証します。TokenRequest はその bridge です。

## pull-ref レジストリホストはクライアントエンドポイントから分離されている

イメージの ID は**リポジトリパス**です。ホスト名は接続元ごとに異なる接続先情報です。クライアント、ビルドエンジン、ノードが同じループバックを共有しなくなると、これが重要になります。ビルドエンジンは Pod の*中*からプッシュする一方、ノードの containerd はノード DNS を使って*ホスト*ネットワークからプルします。そしてポート転送エンドポイント (`127.0.0.1:<ephemeral>`) はノードからプルできません。

そのため、デプロイイメージホストは control-plane エンドポイントとは別に解決されます。明示的な上書き (`--registry` / `CORNUS_REGISTRY` / プロファイルフィールド) が優先され、それがなければサーバーの auth-exempt info エンドポイントが通知する値、それもなければクライアントエンドポイントのホストになります。通知される値は `CORNUS_ADVERTISE_REGISTRY`、または kubernetes バックエンドが自分自身のサービスを introspect したものです。ただし auto-advertise されるのは **NodePort / LoadBalancer のみ**です。ノード containerd はクラスター DNS ではなくホスト DNS を使うため、ClusterIP name はプル時に解決できません。サーバーは、対象ホストが advertised ホストと一致するビルドを、co-located レジストリへ loopback 経由で **push-redirect** します。repository パスは固定されたままなので、プッシュとプルは異なるアドレスから同じ content に到達します。

## 関連ページ

- [Compose, devcontainers & docker](/ja/guides/compose-devcontainers-docker) - ワークフロー。
- [リモートクラスターで作業する](/ja/guides/remote-clusters) - プロファイルと kube-auth の設定。
- [接続設定](/ja/reference/connection-config) - プロファイルファイル format。
- [レジストリガイド](/ja/guides/registry) - クラスターランタイムにレジストリを通知する方法。
- [cornus compose](/ja/cli/compose) · [cornus daemon](/ja/cli/daemon) · [cornus config](/ja/cli/config)
