# インストール

Cornus は単一の Go バイナリです。同じバイナリがサーバー (`cornus serve`) として動作し、クライアント (`cornus build`、`cornus deploy`、`cornus compose` など) としてサーバーを操作します。ビルド済みの CLI を導入する、公開済みのコンテナイメージを実行する、またはソースからビルドする方法を選べます。

::: warning 1.0 未満
Cornus は活発に開発中であり、CLI または API の安定性はまだ保証していません。リリースアーティファクトをバージョンに固定し、アップグレード前にリリースノートを確認してください。
:::

## ビルド済み CLI バイナリ

ビルド済みバイナリは、各 [GitHub Release](https://github.com/moriyoshi/cornus/releases) に `SHA256SUMS` マニフェストおよびキーレス cosign バンドル (`SHA256SUMS.bundle`) とともに添付されています。

| プラットフォーム | アセット |
| --- | --- |
| Linux | `cornus-linux-amd64`、`cornus-linux-arm64` |
| macOS | `cornus-darwin-amd64`、`cornus-darwin-arm64` |
| Windows | `cornus-windows-amd64.exe` |

どのバイナリも自己完結型で、必要なものが最初から入っています。

- [`cornus web`](/ja/cli/web) が使う Web アプリケーションが組み込まれているため、UI の実行に Node.js は必要ありません。
- 組み込みの OpenTelemetry Collector と [組み込みオブザーバビリティストア](/ja/guides/observability) が含まれているため、`cornus serve` は追加のフラグもインストール作業もなしにワークロードのログ、トレース、メトリクスを記録します。バイナリに何が含まれているかは `cornus version --features` で確認できます。

Linux のバイナリは **完全に静的** なので、Alpine や distroless を含むあらゆるディストリビューションで動作します。オブザーバビリティストアを同梱しているため、リリースのダウンロードサイズは素の CLI よりかなり大きく、現在はプラットフォームに応じて約 86〜107 MB です。`--no-obs` は実行時の記録を無効にしますが、ダウンロードするバイナリのサイズは小さくなりません。

バイナリと検証用ファイルをダウンロードし、署名付きチェックサムマニフェストを検証してから、バイナリを `PATH` に配置します。

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus-linux-amd64
curl -fsSLO https://github.com/moriyoshi/cornus/releases/latest/download/SHA256SUMS
curl -fsSLO https://github.com/moriyoshi/cornus/releases/latest/download/SHA256SUMS.bundle
cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity-regexp '^https://github\.com/moriyoshi/cornus/\.github/workflows/release\.yml@refs/tags/v[0-9].*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
grep ' cornus-linux-amd64$' SHA256SUMS | sha256sum -c -
mv cornus-linux-amd64 cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

arm64 では `amd64` を `arm64` に置き換えてください。

## コンテナイメージ

ビルド済みのマルチアーキテクチャ (amd64/arm64) イメージは、リリースワークフローにより GHCR へ公開されます。

* `ghcr.io/moriyoshi/cornus:<version>` は `v*` タグで公開されます (`latest` および `<major>.<minor>` のタグも付与)

イメージにはサードパーティーライセンスの帰属表示が含まれます。提供される Kubernetes マニフェストと Helm チャートはこのイメージをデプロイしますが、ローカル Docker コンテナとして直接実行することもできます。

### ローカル Docker コンテナとして実行する

プロセス内ビルドエンジンのためにサーバーを特権で起動し、`dockerhost` デプロイバックエンドがこのホスト上でコンテナを実行できるよう Docker ソケットをマウントします。

```sh
docker run -d --name cornus --privileged -p 5000:5000 \
  -v cornus-data:/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/moriyoshi/cornus:latest          # サーバーは http://localhost:5000
```

Compose を使う場合:

```yaml
services:
  cornus:
    image: ghcr.io/moriyoshi/cornus:latest
    container_name: cornus
    privileged: true
    ports:
      - "5000:5000"
    volumes:
      - cornus-data:/var/lib/cornus
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "cornus", "version"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  cornus-data:
```

`privileged: true` はプロセス内ビルドエンジン (runc + overlayfs + ユーザー名前空間) に必要です。ルートレスの代替方法と完全な権限モデルは[権限の考え方](/ja/reference/deploy-backends)を参照してください。`/var/lib/cornus` には永続的なボリュームを使用してください。詳しくは[データディレクトリと永続化](/ja/reference/storage-backends)を参照してください。

## Kubernetes で実行する

レジストリの CAS とビルドキャッシュが再起動後も残るよう、Cornus を StatefulSet としてクラスター内にデプロイします。

```sh
# 推奨: OCI レジストリの Helm (イメージタグはチャートバージョンに追従):
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus

# または生のマニフェスト / チェックアウト済みチャート:
kubectl apply -f deploy/k8s/cornus.yaml
helm install cornus deploy/helm/cornus
```

- マニフェストには `StatefulSet` + PVC (データは `/var/lib/cornus`)、`Service`、`ServiceAccount`、`Role`/`RoleBinding` RBAC が含まれます。マニフェストとチャートのどちらも `CORNUS_DEPLOY_BACKEND=kubernetes` (Helm 値は `deployBackend`) を設定するため、サーバーは自身の名前空間へデプロイします。ヘルスチェックとレディネスチェックには `/healthz` と `/readyz` を使います。
- 知っておくとよいチャート値は、`storage` (`CORNUS_STORAGE`。空なら CAS は Pod ごとの PVC に保持)、`replicas` (複数レプリカのハブには `s3://` の `storage` URL が必要)、および対応する JWT 検証環境変数を設定する `auth.jwt.*` です。全項目は[Helm チャート値](/ja/reference/helm-values)のリファレンスにあります。

::: tip
新しいシングルノードクラスターでの提供 → ビルド → デプロイの完全な手順は、[クイックスタート](/ja/introduction/quick-start)を参照してください。
:::

## ソースからビルドする

ビルドには Go 1.26 が必要です。完全に静的でコンテナ実行向けのバイナリは次のように作成します。

```sh
CGO_ENABLED=0 go build -tags "netgo osusergo" -o cornus ./cmd/cornus
```

Google Cloud ストレージ (`gs://`) と Azure ブロブ (`azblob://`) のレジストリストレージバックエンドも有効にするには、`cloudblob` ビルドタグを追加します (既定のビルドでは、これらのスキームに対して「このビルドではサポートされない」という明確なエラーが返ります)。

```sh
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
```

::: warning
プロセス内ビルドエンジンは Linux 専用で、大きな BuildKit 依存ツリーを取り込みます。`go build` が実行できる環境ならどこでもコンパイルできますが、ビルドを実行するには root、またはルートレスユーザー名前空間のスタックが必要です。レジストリとデプロイのサブシステムには特別な権限は不要です。権限の考え方は[アーキテクチャ概要](/ja/architecture/)を参照してください。
:::

## 次の手順

* [クイックスタート](/ja/introduction/quick-start) — Compose プロジェクトを提供、ビルド、デプロイします。
* [Cornus とは](/ja/introduction/what-is-cornus) — 三つのサブシステムとその連携。
