# ローカル Compose プロジェクトをそのまま Kubernetes へ配信する

## シナリオ

チームが毎日ローカルで実行している `compose.yaml` があり、共有ステージング環境や統合実行のために**同じファイル**を実際の Kubernetes クラスターで使いたいとします。デプロイメント、サービス、PVC に書き換える必要はありません。Cornus では Compose ファイルがどのバックエンドでも実行中の制御対象となるため、移行はソースの変更ではなく接続プロファイルの変更です。

## 使用するもの

- サーバーに対してビルドとデプロイを行う Compose 互換クライアント。[Compose、Dev Container、Docker CLI](/ja/guides/compose-devcontainers-docker)と[`cornus compose`](/ja/cli/compose)を参照。
- ローカルサーバーからクラスター内サーバーへ切り替える接続プロファイル。[リモートクラスターで作業する](/ja/guides/remote-clusters)を参照。
- Compose の概念をネイティブ仕様へ変換するデプロイエンジン。[デプロイスペック](/ja/reference/deploy-spec)と[デプロイバックエンド](/ja/reference/deploy-backends)を参照。

## 手順

1. **すでに実行している Compose ファイルから始めます。** user ネットワーク上で API を通じて database と通信する web front end を含む、通常の multi-service プロジェクトです。

   ```yaml
   # compose.yaml
   name: shop
   services:
     web:
       build: ./web
       ports:
         - "8080:80"
       depends_on:
         - api
       networks:
         - frontend
     api:
       build: ./api
       environment:
         DATABASE_URL: postgres://db:5432/shop
       networks:
         - frontend
         - backend
     db:
       image: postgres:16
       volumes:
         - db-data:/var/lib/postgresql/data
       networks:
         - backend
   networks:
     frontend:
     backend:
   volumes:
     db-data:
   ```

2. **現在とまったく同じようにローカルで実行します。** ローカル Cornus サーバー (既定の `dockerhost` バックエンド) に対して、`cornus compose up` は `build:` サービスをビルドし、stack をデプロイし、公開ポートを `127.0.0.1:8080` で保持します。

   ```sh
   cornus compose up --build
   # -> forwarding 127.0.0.1:8080 -> :80 ; curl http://127.0.0.1:8080 answers
   ```

3. **プロファイルの対象をクラスターにします。** クラスター内サーバーを一度保存します。イングレスのないクラスターでは、そのサービスを指定して CLI がコマンドごとにポート転送を開くようにします。

   ```sh
   cornus config set-context staging \
     --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
   cornus config use-context staging
   ```

4. **同一コマンドをクラスターに対して実行します。** ファイルもコマンドも同じです。違いは選択したプロファイルだけで、`CORNUS_DEPLOY_BACKEND=kubernetes` を持つクラスター内サーバーに解決されます。

   ```sh
   cornus compose up --build
   ```

   `build:` サービスはクラスター内でビルドされ同梱レジストリへプッシュされます。各サービスは `shop-web` / `shop-api` / `shop-db` というデプロイメントと、公開ポート用のサービスになります。`frontend` / `backend` ユーザーネットワークもクラスター上に実現されます。セッションの存続期間中は `8080` がマシンの `127.0.0.1:8080` へ自動転送されるため、ワークロードがクラスター内で実行されていても curl http://127.0.0.1:8080 が応答します。

5. **同じ方法で確認し、削除します。**

   ```sh
   cornus compose ps
   cornus compose logs --follow web
   cornus compose down --volumes     # --volumes also removes the db-data PVC
   ```

## 仕組み

Compose ファイルは内部でネイティブ [デプロイスペック](/ja/reference/deploy-spec) へ変換され、サーバーが実行するバックエンドに同じ仕様が適用されます。そのためすべての核となる概念は変更なしで引き継がれます。

- **サービス** は `<project>-<service>` という名前のデプロイメント一つずつになります。
- **`ports:`** は公開ポートになります。セッション中は Kubernetes を含むすべてのバックエンドで `127.0.0.1:<host>` へ自動転送されるため、ワークロードは localhost で応答します。ポートごとのリスナー (既定) を選ぶか、`--conduit` でサービス名に到達する単一 SOCKS5 プロキシを選べます。
- **`networks:`** はユーザー定義ネットワークになります。同じネットワークのメンバーはサービス名 (およびエイリアス) で相互に解決します。Kubernetes では既定ドライバーは `services` (DNS のみ、どのクラスターでも利用可) です。`bridge` / `ipvlan` / `macvlan` (Multus) または `cilium` は `CORNUS_K8S_NET_DRIVER` で明示的に有効化します。
- **`volumes:`** は管理対象ボリュームになります。名前付きボリュームは一つのデプロイメントを削除しても存続するプロジェクトスコープのストアです (Kubernetes では PVC、`dockerhost` では Docker 名前付きボリューム)。匿名ボリュームは一時的です。
- **`depends_on`**、**`healthcheck`**、**`deploy.replicas`**、**`deploy.update_config`** も変換されます。

バックエンドはサーバー側で選択されるため、CLI 側ワークフローは `dockerhost`、`containerd`、`bare`、`kubernetes` のいずれでも同一です。[デプロイ backends](/ja/reference/deploy-backends)を参照してください。

### Kubernetes で異なる点

いくつかの Compose 設定には Kubernetes の同等物がなく、フィールドごとに扱われます ([デプロイスペックリファレンス](/ja/reference/deploy-spec)が各項目を示します)。

- ポートの `hostIP` (Compose の `127.0.0.1:8080:80`) はホストバックエンドでは尊重されますが、Kubernetes サービスには同等物がありません。
- UDP 公開ポートは `dockerhost` / `containerd` / `bare` では動作しますが、Kubernetes ポート転送は TCP 専用なので `/udp` mapping はそこでスキップされます。
- ヘルスチェックは `dockerhost` では Docker ヘルスチェック、Kubernetes では exec liveness / readiness probe になります。
- `deploy.update_config` は Kubernetes デプロイメントの `strategy.rollingUpdate` にだけ対応します。ホストバックエンドは一つのインスタンスを再作成します。
- Compose `labels:` は Kubernetes では label ではなく pod-template **annotation** になります。多くの host-only knob (`init`、`stop_signal`、`ulimits`、`devices` など) は警告とともに Kubernetes で無視されます。

## バリエーション

- **Detached staging。** `cornus compose up --build -d` はマウントと転送ポートをバックグラウンド helper に渡して戻ります。`cornus compose down` であとから停止します。
- **サービス名で到達する。** `cornus compose up --conduit socks5` はポートごとのリスナーを一つのプロキシに置き換えるため、`web.cornus.internal` と `db.cornus.internal` がそれを通じて解決されます。
- **layered 上書き。** base `compose.yaml` を保ち、クラスター専用調整は `-f compose.staging.yaml` として追加します。コマンドは同じです。

**関連項目:** [Compose、devcontainers、docker CLI](/ja/guides/compose-devcontainers-docker) · [ワークロードをデプロイする](/ja/guides/deploying-workloads) · [リモートクラスターで作業する](/ja/guides/remote-clusters) · [デプロイスペック](/ja/reference/deploy-spec) · [デプロイ backends](/ja/reference/deploy-backends)
