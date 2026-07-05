# クイックスタート

Cornus は主にローカルの Kubernetes クラスター内で動かすことを想定しています。この手順では、何も導入されていない状態から、単一ノードの [k3s](https://k3s.io/) クラスターでワークロードを実行するところまで進めます。ビルド済みの `cornus` バイナリと、`ghcr.io/moriyoshi/cornus` で公開しているマルチアーキテクチャイメージを使用します。

Git のクローン、Go ツールチェーン、Docker は不要です。k3s は containerd をネイティブに実行し、Cornus のクラスター内ビルドエンジンがデモイメージをビルドします。また、`cornus compose` はサーバーと直接通信するため、一連の処理に Docker デーモンは必要ありません。必要なのは通常の `compose.yaml` と一つのコマンドだけです。

## 1. Cornus CLI を導入する

使用するプラットフォーム向けのビルド済み静的バイナリをダウンロードし、`PATH` に配置します。

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

(arm64 では `amd64` を `arm64` に置き換えます。) コンテナイメージとソースからのビルドについては、[インストール](/ja/introduction/installation) を参照してください。

## 2. k3s と Cornus を導入し、CLI の接続先を設定する

Cornus は固定の NodePort (`30500`) で公開されるため、CLI とノード上の containerd は実際のサービスエンドポイントを介してアクセスできます。ここでは `kubectl port-forward` に依存しません。まず、k3s の containerd に `localhost:30500` が HTTP のレジストリであることを設定します (手順 3 でビルドするデモイメージはここから提供されます)。次に k3s を導入します。

```sh
sudo mkdir -p /etc/rancher/k3s
sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
EOF
curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
```

次に Cornus を導入します。同梱のマニフェストは、特権 StatefulSet (ビルドエンジンに必要です)、PVC、クラスターへのデプロイ用 RBAC、NodePort `Service` (`30500` からコンテナの `5000`) で構成されます。このマニフェストは公開済みの GHCR イメージをすでに参照しているため、ビルドは不要です。ノード上の containerd が `ghcr.io/moriyoshi/cornus` を GHCR から直接プルします。

```sh
kubectl apply -f https://raw.githubusercontent.com/moriyoshi/cornus/main/deploy/k8s/cornus.yaml
# 推奨する Helm の方法: OCI レジストリからチャートを直接導入します。
#   helm install cornus oci://ghcr.io/moriyoshi/charts/cornus --version 0.1.0
kubectl rollout status statefulset/cornus --timeout=300s
```

サーバーが起動し、NodePort で要求を受け付けられることを確認します。

```sh
curl http://localhost:30500/healthz        # -> {"status":"ok"}
```

後続のコマンドで `--server` や `CORNUS_HOST` を指定しなくてよいよう、既定の接続プロファイルとして保存します。

```sh
cornus config set-context demo --server http://localhost:30500
cornus config use-context demo
```

リモートクラスターまたはイングレスなしのクラスターへの接続については、[接続設定](/ja/reference/connection-config) と [リモートワークフロー](/ja/topics/remote-workflows) を参照してください。

## 3. Compose ファイルでビルドしてデプロイする

Cornus CLI は Compose に対応しています。`docker compose` が読み込むものと同じ通常の `compose.yaml` を作成します。`build:` セクションではキャッシュマウントとシークレットマウントを使用し、一つのポートを公開します。

```sh
mkdir -p demo
tee demo/Dockerfile >/dev/null <<'EOF'
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl busybox-extras
RUN --mount=type=secret,id=token \
    test -f /run/secrets/token && echo "secret present (not stored in image)"
RUN mkdir -p /www && echo 'cornus demo' > /www/index.html
CMD ["sh", "-c", "echo cornus demo && exec httpd -f -v -p 80 -h /www"]
EOF
echo -n s3cret > /tmp/token

tee demo/compose.yaml >/dev/null <<'EOF'
name: demo
services:
  web:
    build:
      context: .
      secrets:
        - token
    ports:
      - "8080:80"
secrets:
  token:
    file: /tmp/token
EOF
```

起動します。一つのコマンドで、クラスター内のイメージをビルドし (コンテキストとシークレットは 9P-on-WebSocket で Cornus Pod にストリームされるため、ホストにビルド権限や Docker は不要です)、Cornus のクラスター内レジストリへプッシュし、デプロイし、公開ポートをローカルマシンへ転送します。

```sh
cd demo
cornus compose up
```

サービスは `localhost:30500/demo-web:latest` としてビルド、デプロイされます (参照名は `<project>-<service>` であるため、`build:` を使うサービスは `image:` を自ら設定しません)。コマンドはフォアグラウンドでセッションを維持し、クライアントローカルのマウントをストリームするとともに、ワークロードの公開ポートをトンネルします。そして `forwarding 127.0.0.1:8080 -> :80` と表示します。デモコンテナは `:80` でページを提供するため、ワークロードがクラスター内で動作していても、`curl http://127.0.0.1:8080` は `cornus demo` を返します。コマンドはそのまま実行しておきます。

## 4. 確認して後片付けする

別の端末から実行します (ワークロード名は `<project>-<service>` です)。

```sh
kubectl get deployment,service demo-web
kubectl logs deployment/demo-web           # -> cornus demo
cornus compose logs demo-web               # 同じログを表示します。kubectl は不要です。
```

`cornus compose logs` は各サービスのログをストリームします。追跡するには `--follow` を追加します。`--tail`、`--since`、`-t` でログの件数、時刻、タイムスタンプを指定でき、サービス名を指定すれば絞り込めます (既定ではすべてのサービスが対象です)。

続いて環境を削除します。フォアグラウンドの `cornus compose up` を Ctrl-C で終了して公開ポートのトンネルを解放し、サービスとクラスターを削除します。

```sh
cornus compose down
/usr/local/bin/k3s-uninstall.sh
rm -rf demo /tmp/token
```

::: tip バリエーション
同じ手順は k0s (単一バイナリの containerd)、kind (ノードポートをマッピングするか、ビルドとデプロイの間にイメージを読み込む)、通常の Docker ホスト (Docker ソケットをマウントした `dockerhost` バックエンド)、素の containerd ホスト (`CORNUS_DEPLOY_BACKEND=containerd`) でも利用できます。[デプロイバックエンド](/ja/reference/deploy-backends) を参照してください。
:::

## エンジンを直接操作する

`cornus compose up` は、ビルドエンジンとデプロイエンジンという二つの基本操作をまとめた糖衣構文です。明示的に制御したい場合、Compose ファイルがない場合、あるいは操作の間に手順を挟む必要がある場合には、これらを直接実行できます。

```sh
# クラスター内でビルドし、レジストリへプッシュします。--builder はコンテキストと
# シークレットを 9P-on-WebSocket で Cornus Pod へストリームするため、ホストに Docker
# やビルド権限は不要です。
cornus build --builder ws://localhost:30500/.cornus/v1/build/attach \
  -t localhost:30500/demo:v1 \
  --secret id=token,src=/tmp/token demo

curl http://localhost:30500/v2/demo/tags/list    # -> {"name":"demo","tags":["v1"]}

# ネイティブ仕様からデプロイします。これは上位の各操作が変換先とするスキーマです。
# 現在の接続プロファイルを使用します (`--server` を明示するとそちらが優先されます)。
tee demo.yaml >/dev/null <<'EOF'
name: demo
image: localhost:30500/demo:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
EOF
cornus deploy -f demo.yaml
```

完全なフィールド一覧については、[`cornus build`](/ja/cli/build)、[`cornus push`](/ja/cli/push)、[`cornus deploy`](/ja/cli/deploy)、[デプロイスペックリファレンス](/ja/reference/deploy-spec) を参照してください。

## 次のステップ

* [出力モード](/ja/guides/output-modes): CI には `plain`、エージェントには `json` を選択します。
* [リモートワークフロー](/ja/topics/remote-workflows): CLI の接続先をリモートクラスターにします。
* [トンネル](/ja/guides/tunnels): ワークロードをパブリックに公開します。
* [ワークロード間 hub](/ja/topics/hub): ほかのワークロードへ名前で到達します。
