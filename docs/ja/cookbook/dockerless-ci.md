# CI から Docker なしでビルドとデプロイを行う

## シナリオ

CI パイプライン (または k3s / containerd ノード) でイメージをビルドし、クラスターへロールアウトする必要があるものの、どこにも Docker デーモンがないとします。`dockerd`、`buildkitd`、個別に用意したレジストリは不要です。クラスター内 Cornus サーバーが三つの役割をすべて担います。プロセス内 BuildKit エンジンがイメージをビルドし、同梱 OCI レジストリが保存し、ランタイム (containerd / Kubernetes) がプルします。CI ランナーに必要なのは `cornus` バイナリとソースツリーだけです。

## 使用するもの

- ランナーから 9P 経由で操作するプロセス内ビルドエンジン。[イメージをビルドする](/ja/guides/building-images)を参照。
- ビルド済みイメージを保存する同梱 OCI レジストリ。[レジストリとストレージ](/ja/guides/registry)を参照。
- `kubernetes` (または `containerd`) バックエンド上の命令的デプロイエンジン。[ワークロードをデプロイする](/ja/guides/deploying-workloads)と[デプロイバックエンド](/ja/reference/deploy-backends)を参照。
- パイプラインのステップでコマンドラインの設定を不要にする接続プロファイル。[`cornus config`](/ja/cli/config)を参照。

## 手順

1. **ランナーの対象をクラスター内サーバーに一度設定します。** 接続プロファイルを保存すると、以後のステップはコマンドラインの指定なしにエンドポイントを解決します。イングレスのないクラスターでは Service への専用ポート転送も開きます。サーバーを指定するプロファイルはリモートビルドも自動的にそこへ向けます。

   ```sh
   cornus config set-context ci \
     --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
   cornus config use-context ci
   ```

   到達可能な URL なら `--server http://cornus.example:5000` でも同じです。CI ではランナー自身の Kubernetes へのアクセス権からベアラートークンを発行する (`--kube-auth-service-account` / `--kube-auth-audience`) か、静的な `--token` を渡します。

2. **クラスター内でビルドし、同梱レジストリへプッシュします。** ランナーはビルドコンテキストとシークレットを 9P-over-WebSocket でサーバーへストリームします。プロセス内 BuildKit エンジンがビルドし、結果を Cornus と同居するレジストリへプッシュします。ランナーに Docker やビルド権限は不要です。

   ```sh
   cornus build --builder ws://cornus.example:5000/.cornus/v1/build/attach \
     -t cornus.example:5000/app:$CI_COMMIT_SHA \
     --secret id=npmrc,src=$HOME/.npmrc \
     --rootless ./context
   ```

   `--rootless` (またはサーバー全体の `CORNUS_ROOTLESS`) は user 名前空間内でビルドを実行します。プロファイルがすでにサーバーを指定していれば、`--builder` は省略でき、ビルドは自動的にリモートへ向かいます。ビルドが実際に読む byte だけをプルするには、名前付きビルドコンテキストに `--lazy` を追加します。

3. **ビルド直後のイメージをデプロイします。** 同じサーバーにネイティブデプロイスペックを適用します。`kubernetes` バックエンドではデプロイメントとサービスが生成され、ノードの containerd が同梱レジストリからイメージをプルします。

   ```yaml
   # deploy.yaml
   name: app
   image: cornus.example:5000/app:$CI_COMMIT_SHA
   replicas: 3
   restart: unless-stopped
   ports:
     - { host: 8080, container: 80 }
   updateConfig:
     parallelism: 1
     order: start-first
   healthcheck:
     test: ["CMD", "curl", "-f", "http://localhost/healthz"]
     interval: 30s
     retries: 3
   ```

   ```sh
   envsubst < deploy.yaml > deploy.rendered.yaml
   cornus deploy -f deploy.rendered.yaml --server http://cornus.example:5000 --detach
   ```

   `--detach` は仕様を POST して戻り、クライアントセッションなしでワークロードを実行し続けます。後続処理を待たないパイプラインステップに適したモードです。完了後は `cornus deploy -f deploy.yaml --delete --server ...` でデプロイメントを削除します。

4. **Compose なら一つのコマンドで両方を実行できます。** プロジェクトに `compose.yaml` がすでにあり、そこに `build:` セクションが含まれる場合、`cornus compose up --build` はクラスター内で各サービスイメージをビルド、プッシュ、デプロイします。デーモンを使わない同じ処理を一つのコマンドで実行できます。

   ```sh
   cornus compose up --build -d
   ```

## 仕組み

三つのサブシステムは、OCI HTTP だけで連携します。ビルドエンジンがイメージ参照をレジストリへプッシュし、対象ランタイムがプルします。この流れに Docker デーモンはありません。ビルドエンジンは `docker buildx` と**同じ** BuildKit ソルバーをプロセス内に組み込むため、キャッシュマウント、シークレットマウント、SSH 転送、名前付きビルドコンテキストはすべてそのまま使えます。詳しくは[イメージをビルドする](/ja/guides/building-images)を参照してください。リモートビルドでは、ランナーが一つの WebSocket 上で読み取り専用の 9P サーバーを実行し、エンジンがコンテキストを必要なときに読み取ります。NAT 配下のプライベート CI ランナーも、何も外部公開する必要がありません。

結果を保存するレジストリは、Cornus に同梱された OCI Distribution レジストリです。イメージのタグに使う参照先は、サーバーがクラスターノードへ通知するレジストリホストです。参照先は `--registry` / `CORNUS_REGISTRY`、サーバーの `GET /.cornus/v1/info`、エンドポイントホストの順に解決されます。複数ノードのクラスターでは、ノードの containerd がそのホストを解決して信頼できなければなりません。HTTP として扱うか、TLS を提供してください。詳しくは[レジストリとストレージ](/ja/guides/registry)を参照してください。

デプロイエンジンは、選択したバックエンドに対して命令的に仕様を適用します。`kubernetes` はデプロイメントとサービスを生成します。`containerd` は CNI ブリッジネットワークを備えた素の containerd ホスト上で、dockerd なしにワークロードをネイティブに実行します。`containerd` バックエンドでは、containerd **ビルドワーカー** (`CORNUS_BUILD_WORKER=containerd`) を組み合わせると、ビルド直後のイメージがホストのイメージストアに直接入り、レジストリを往復せずにデプロイできます。バックエンド一覧は[デプロイバックエンド](/ja/reference/deploy-backends)を参照してください。

## バリエーション

- **Kubernetes なしの素の containerd ノード。** サーバーを root で `CORNUS_DEPLOY_BACKEND=containerd CORNUS_BUILD_WORKER=containerd` として実行します。cornus compose up --build がホスト自身の containerd にビルドとデプロイを行います。
- **外部レジストリ。** すでに運用しているレジストリ向けにビルドをタグし、デプロイプル ref がそこを指すよう `CORNUS_REGISTRY` を設定します。そのほかの flow は同じです。
- **実行間でのレジストリキャッシュ。** `--cache-to` / --cache-from type=registry,ref=... を追加すると、コールドスタートの CI ランナーが前回ビルドのキャッシュを再利用できます。

**関連項目:** [クックブック](/ja/cookbook/) · [イメージをビルドする](/ja/guides/building-images) · [ワークロードをデプロイする](/ja/guides/deploying-workloads) · [レジストリとストレージ](/ja/guides/registry) · [リモートクラスターで作業する](/ja/guides/remote-clusters) · [デプロイバックエンド](/ja/reference/deploy-backends)
