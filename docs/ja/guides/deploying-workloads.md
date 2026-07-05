# ワークロードをデプロイする

[デプロイスペック](/ja/reference/deploy-spec) を [cornus deploy](/ja/cli/deploy) で適用し、ワークロードに接続し、三つの[デプロイバックエンド](/ja/reference/deploy-backends)を使い分けるためのレシピです。ローカルバックエンドは `CORNUS_DEPLOY_BACKEND` 環境変数で選ばれます。CLI フラグはありません。

## Compose プロジェクトを Docker ホストにローカルデプロイする (既定 dockerhost バックエンド)

既定の `dockerhost` バックエンドで、仕様をローカル Docker デーモンに適用します。

```sh
cornus deploy -f app.yaml
```

```yaml
name: web
image: localhost:5000/app:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
```

- `dockerhost` には Docker ソケット (`/var/run/docker.sock`) が必要です。これは最も機能が豊富なバックエンドであり、最も多くの仕様フィールドに対応します。

**関連ページ:** [cornus deploy](/ja/cli/deploy), [デプロイスペック](/ja/reference/deploy-spec), [デプロイ backends](/ja/reference/deploy-backends)

## 素の containerd ホストにデプロイする (CORNUS_DEPLOY_BACKEND=containerd)

dockerd を介さず、containerd ホスト上でワークロードをネイティブに実行します。

```sh
sudo CORNUS_DEPLOY_BACKEND=containerd cornus deploy -f app.yaml
```

- Linux 専用です。root が必要です (netns を作成し CNI を実行します)。containerd ソケット (`CORNUS_CONTAINERD_ADDRESS`、既定 `/run/containerd/containerd.sock`) と、`/opt/cni/bin` 下の標準 CNI プラグインも必要です。
- dockerhost と比べた既知の制約: attach は出力専用で、ヘルスチェックは無視されます。

**関連ページ:** [デプロイ backends](/ja/reference/deploy-backends), [cornus deploy](/ja/cli/deploy)

## Kubernetes クラスターにデプロイする (サーバー / 接続プロファイル経由)

`kubernetes` バックエンドはサーバー / クラスター内のみなので、クラスター内で動く cornus サーバーに対してデプロイします。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

- ローカルの `cornus deploy` に `CORNUS_DEPLOY_BACKEND=kubernetes` を付けても警告とともに `dockerhost` へフォールバックします。クラスターバックエンドはサーバー (`cornus serve`) 上で動きます。
- サーバーを接続プロファイルとして一度保存しておくと、以後のコマンドで `--server` が不要になります。

**関連ページ:** [リモート clusters](/ja/guides/remote-clusters), [デプロイ backends](/ja/reference/deploy-backends), [リモート workflows](/ja/topics/remote-workflows)

## 生のデプロイスペックファイルを適用する (cornus deploy -f spec.yaml)

ネイティブ schema を直接デプロイします。Compose と devcontainers が変換される先と同じ shape です。

```sh
cornus deploy -f spec.yaml
```

- 仕様は命令的に適用されます。1 つの仕様を受け取り、バックエンドがワークロードをその内容に収束させます。ポート、マウント、ボリューム、リソース、ヘルスチェックについては完全なフィールドリファレンスを参照してください。

**関連ページ:** [デプロイスペック](/ja/reference/deploy-spec), [cornus deploy](/ja/cli/deploy)

## デプロイメントを削除する (cornus deploy --delete / cornus compose down)

名前を指定してデプロイメントを削除します。ローカルでもサーバーに対してでも使えます。

```sh
cornus deploy -f app.yaml --delete
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

- Compose プロジェクトでは、代わりに `cornus compose down` を使います (project-scoped 名前付きボリュームも削除するには `--volumes` を追加)。

**関連ページ:** [cornus deploy](/ja/cli/deploy), [cornus compose](/ja/cli/compose)

## バックグラウンドでデプロイを実行する (-d/--detach)

仕様をサーバーへ一度 POST して終了し、クライアントセッションなしでワークロードを動かし続けます。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --detach
# later, tear it down:
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

- detached デプロイはクライアントローカルバインドマウントと client-sourced 資格情報を拒否し、公開済みポートは自動転送ではなくサーバーホストにバインドされます。
- `--detach` はローカルデプロイでは no-op です。

**関連ページ:** [cornus deploy](/ja/cli/deploy), [リモート workflows](/ja/topics/remote-workflows)

## レプリカ数とローリング更新を設定する (デプロイスペックの replicas と updateConfig)

desired インスタンス count と、Kubernetes ローリング更新の進み方を設定します。

```yaml
name: web
image: localhost:5000/app:v1
replicas: 3
updateConfig:
  parallelism: 1
  order: start-first
```

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

- `replicas` はすべてのバックエンドで尊重されます。ホストバックエンドでは公開済みホストポートはレプリカ 0 にだけ向きます。
- `updateConfig` は Kubernetes デプロイメントの `strategy.rollingUpdate` にだけ map されます。ホストバックエンドは単一インスタンスを recreate し、これを無視します。

**関連ページ:** [デプロイスペック](/ja/reference/deploy-spec), [デプロイ backends](/ja/reference/deploy-backends)

## 実行中ワークロードの中でコマンドを実行する (cornus exec)

`docker exec` のように、サーバー経由でデプロイメントの first インスタンスに exec します。

```sh
cornus exec --server https://cornus.example.com -it web -- sh
```

- デプロイメント name の後ろにあるものはすべてコマンドにそのまま渡されます。`-i` は stdin を転送し、`-t` は PTY を要求します (stdin が terminal でない場合はプレーンストリームに downgrade)。
- リモートコマンドの exit code は cornus 自身の exit code として伝搬します。

**関連ページ:** [cornus exec](/ja/cli/exec), [cornus config](/ja/cli/config)

## クライアントローカルディレクトリをリモートワークロードにマウントする (--local-mount、9P でストリーム)

自分の machine 上にあるディレクトリを、リモートサーバー上で動くワークロードに bind-mount します。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --local-mount ./config:/etc/app:ro \
  --local-mount ./data:/data
```

- `--local-mount SRC:DST[:ro]` は繰り返し指定可能で、セッションの存続中にパスを 9P で提供します。ワークロードはファイルをその場で読みます。事前コピーはありません。
- `,cache` を追加すると、不変の読み取り専用ソースとして宣言します。サーバーのファイル単位キャッシュを使用し、`:ro` を暗黙に指定します。
- `,async` を追加すると、block protocol による書き込み可能でキャッシュ整合性を保つマウントになります。開発用データベースのような書き込み集約的な単一 writer ワークロード向けで、`replicas: 1` が必要です。`ro` または `cache` とは併用できません。
- データベース型の async マウントでは、サーバーと deploy caller の両方で `CORNUS_BLOCK_COHERENCE=subhash,subfill` を設定し、`CORNUS_BLOCK_READAHEAD=64k` 以上の cap を追加することが出発点です。[サーバー環境変数](/ja/reference/server-env-vars)を参照してください。
- フォアグラウンドセッションが必要です。`--detach` はクライアントローカルマウントを拒否します。

**関連ページ:** [cornus deploy](/ja/cli/deploy), [networking](/ja/guides/networking), [リモート workflows](/ja/topics/remote-workflows)

## 公開済み / 未公開ポートに到達する (自動クライアント側転送と cornus port-forward)

`--server` セッション中は、公開済みポート (仕様の `ports:`) が `127.0.0.1:<host>` へ自動転送されます。他の任意のコンテナポートには、必要に応じて `cornus port-forward` で到達します。

```sh
# Published ports auto-forward for the session's lifetime:
cornus deploy -f app.yaml --server https://cornus.example.com
# (prints forwarding 127.0.0.1:8080 -> :80)

# Reach an unpublished container port separately:
cornus port-forward web 5432:5432
```

- デプロイの `--no-forward-ports` で自動転送を無効化できます。`cornus port-forward` は `LOCAL:REMOTE` (または裸の `PORT`) mapping ごとにローカルリスナーを 1 つバインドし、Ctrl-C までフォアグラウンドで動きます。
- クラスタープロファイルでは、どちらのパスも kubeconfig を使ってワークロード pod へ直接向かい、必要ならサーバー経由のトンネルにフォールバックします。`/udp` mapping は dockerhost、containerd、bare バックエンドでは動作しますが、Kubernetes ではスキップされます。

**関連ページ:** [cornus port-forward](/ja/cli/port-forward), [networking](/ja/guides/networking), [リモート workflows](/ja/topics/remote-workflows)
