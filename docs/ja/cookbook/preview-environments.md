# 一時的なプレビュー環境

## シナリオ

プルリクエストまたは機能ブランチごとに、使い捨ての環境を用意します。ブランチのイメージをビルドし、PR ごとの名前でクラスターにデプロイして、レビュー担当者には VPN、kubeconfig、常設のイングレスなしでブラウザーやスマートフォンから開けるパブリック URL を渡します。PR がマージまたはクローズされると、すべて消えます。Cornus では一つのバイナリでこの一連の処理を完結できます。[`cornus build`](/ja/cli/build)がイメージを生成し、[`cornus deploy`](/ja/cli/deploy)が一意の名前を持つワークロードを起動し、[`cornus tunnel`](/ja/cli/tunnel)がパブリック HTTPS URL を提供します。削除には `--delete` を一つ使うだけです。

## 使用するもの

- [`cornus build`](/ja/cli/build) — プロセス内の BuildKit エンジンでブランチのイメージをビルドし、同梱レジストリへプッシュします。
- [`cornus deploy`](/ja/cli/deploy) — PR ごとの `name` を持つ [デプロイスペック](/ja/reference/deploy-spec) を適用します。
- [`cornus tunnel`](/ja/cli/tunnel) — ワークロードのポートをホスト型中継経由でパブリックインターネットに公開します。
- [パブリックトンネル](/ja/topics/tunnels) — 共有可能な URL の基になるモデルです。

## 手順

以下は接続プロファイルがすでにクラスターサーバーを選択している前提です ([リモートクラスター](/ja/guides/remote-clusters)を参照)。そうでなければ各コマンドに `--server https://cornus.example.com` を追加してください。

**1. すべてを PR 名で命名します。** 一つの変数でイメージタグとデプロイメント名をまとめて扱うため、プレビュー同士が衝突しません。

```sh
PR=123
IMAGE="registry.example:5000/app:pr-${PR}"
NAME="app-pr-${PR}"
```

**2. ブランチのイメージをビルドしてプッシュします。** ビルドはサーバー上で実行されます (`--builder`、またはビルダーを指定するプロファイルを使用)。呼び出し元に Docker やビルド権限は不要です。

```sh
cornus build -t "$IMAGE" .
```

**3. PR ごとの名前でデプロイします。** PR ごとの名前とイメージで仕様を生成し、環境がコマンド終了後も存続するよう、切り離して適用します。

```sh
cat > preview.yaml <<YAML
name: ${NAME}
image: ${IMAGE}
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
YAML

cornus deploy -f preview.yaml --detach
```

**4. パブリック URL を公開します。** `cornus tunnel` はサーバーにワークロードポートのパブリックトンネルをホストさせ、URL を表示します。ワークロードがそのポートを公開していなくても到達できます。

```sh
cornus tunnel --authtoken "$NGROK_AUTHTOKEN" "$NAME" 80
# prints e.g. https://abcd-1234.ngrok-free.app  -- paste into the PR
```

この URL を PR のコメントとして投稿すれば、レビュー担当者が開けます。コマンドは `Ctrl-C` を押すまで動作します。常時稼働のプレビューでは、運用者がサーバーに既定の資格情報 (`CORNUS_TUNNEL_AUTHTOKEN`) を設定し、呼び出し元で `--authtoken` を省略できます。

**5. PR がクローズされたらすべて削除します。** 名前でデプロイメントを削除します (トンネルはそのコマンドの終了とともに消えます)。

```sh
cornus deploy -f preview.yaml --delete
```

## 仕組み

各段階は一つのサーバー側サブシステムに対応します。`cornus build` はサーバー上で BuildKit エンジンを実行し、結果を同梱レジストリへプッシュします。プロファイルまたはサーバーが通知するレジストリ向けにタグされるため、別のレジストリを運用しなくてもデプロイの `image` 参照を解決できます。`cornus deploy --detach` は仕様を一度 POST して終了し、クライアントセッションなしでワークロードを実行し続けます。`name` は PR ごとの値で、管理対象リソースにはその値がラベルとして付くため、適用と削除は冪等であり、異なる PR のプレビューは衝突しません。`cornus tunnel` はワークロードのデプロイ方法に依存しません。Cornus **サーバー**がプロセス内でトンネルをホストし、[`cornus port-forward`](/ja/cli/port-forward)と同じデータブリッジで各入力接続をワークロードへ接続します。そのため Docker ホスト、containerd、Kubernetes のどのバックエンドでも、イングレスなしでポートに到達します。トンネルの資格情報は、すでに認証済みの要求でクライアントが注入するため、サーバーが事前に知る必要はありません。バックエンド (`ngrok` が既定、または `ssh` / `cloudflare` / `tailscale`) はサーバー側で選択します。詳しくは[パブリックトンネル](/ja/topics/tunnels)を参照してください。

デプロイは切り離して実行するため、公開ポートは自分のマシンへ自動転送されず、サーバーホストにバインドされます。ここではパブリックな入口がローカルリスナーではなくトンネルなので、この動作が適しています。切り離したデプロイでは、クライアントローカルのマウントとクライアント由来の資格情報も拒否されるため、この方法で作るプレビューは完全に自己完結します。

## バリエーション

**生の仕様ではなく Compose プロジェクトを使う。** ブランチに Compose ファイルがあるなら、プロジェクト名を PR ごとに設定して切り離して起動し、`down` で削除します。

```sh
cornus compose -p "pr-${PR}" up --build -d
cornus tunnel "pr-${PR}-web" 80
# later:
cornus compose -p "pr-${PR}" down --volumes
```

**HTTP ではなく生の TCP** (データベース、gRPC エンドポイント) を公開します。

```sh
cornus tunnel --proto tcp "$NAME" 5432
```

**トンネルの代わりにクラスターイングレスを使う。** イングレスコントローラーがすでにある Kubernetes クラスターでは、Cornus は各プレビューに `networking.k8s.io/v1` Ingress から直接パブリック URL を与えられます。中継プロセスを維持する必要がなく、URL は切り離したデプロイ後も存続します (トンネルはコマンドが動作する間だけです)。運用者は Helm チャートの `ingress` 値で、一度だけクラスターを設定します。サーバーの既定値である `CORNUS_INGRESS_DOMAIN`、`CORNUS_INGRESS_CLASS`、`CORNUS_INGRESS_TLS_ISSUER`、たとえば `preview.example.com` のようなワイルドカードのプレビュードメイン、イングレスクラス、HTTPS 用の cert-manager クラスター発行者を設定します。すると手順 3 の仕様でイングレスを有効にすればよく、手順 4 は不要になります。ホストはデプロイメント名から自動的に導出されます。

```sh
cat > preview.yaml <<YAML
name: ${NAME}
image: ${IMAGE}
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true          # host auto-derived as <name>.<CORNUS_INGRESS_DOMAIN>
  tls: { }               # HTTPS via the server's default cluster-issuer
YAML

cornus deploy -f preview.yaml --detach
# reviewers browse https://app-pr-123.preview.example.com
```

Compose プロジェクトでは、web サービスに `x-cornus-ingress: {}` を置くだけです。ホストはプロジェクトごとに `<service>.<project>.<domain>` として名前空間化されます。`web.pr-123.preview.example.com` のようなホスト名になり、`-p pr-123` を指定すると、多数のプレビューが一つのベースドメインで衝突せずに共存します。`domain:` や `class_name:` のような共通の上書きは、ファイル先頭のプロジェクトレベルの `x-cornus-ingress:` ブロックに置きます。各サービスは引き続き個別に有効化します。運用者が `CORNUS_INGRESS_ENFORCE_DOMAIN` でドメインを固定していない限り、サーバーの既定値はワークロードごとにクライアントが上書きできます。`hosts:` で複数の名前を前段に置くこともできます (`@` トークンはルートドメインを表します)。イングレスは Kubernetes 専用であり、Docker ホストまたは containerd バックエンドでは警告とともに無視されるため、ファイルは移植可能です。削除方法は変わらず、`--delete` がデプロイメントを削除すると Kubernetes が Ingress もガベージコレクションします。クラスターごとに選択してください。トンネルはイングレスコントローラーなしで任意のバックエンドに使えます。イングレスはクラスターにネイティブで、コマンド終了後も存続しますが、コントローラー、ワイルドカード DNS、HTTPS 用の証明書発行者が必要です。

**CI に組み込みます。** この一連の処理はデーモン不要でスクリプト化できます。ビルド、デプロイ、トンネルを行うパイプラインステップを `pull_request: opened` で実行し、`cornus deploy -f preview.yaml --delete` を `closed` で実行します。トンネル URL は標準出力に表示されるため、取得して PR へ投稿できます。

**関連項目:** [イメージをビルドする](/ja/guides/building-images) · [ワークロードをデプロイする](/ja/guides/deploying-workloads) · [ネットワークのレシピ](/ja/guides/networking) · [トンネル](/ja/guides/tunnels) · [クックブック](/ja/cookbook/)
