# サーバーをセットアップする

`cornus` のコマンドはすべてサーバーと通信します。このページには、サーバーが取りうる構成ごとに短い手順書をまとめています。必要なもの、起動するコマンド、うまくいったかを確認する方法です。[`cornus setup`](/ja/cli/setup) は、選んだ構成のセクションへ直接リンクします。

ここにあるのはリファレンスではなく手順書です。各セクションの末尾には、その主題を網羅的に扱うページへのリンクがあります。フラグ、値、機能の一覧が必要になったらそちらをたどってください。

## どの構成にするか {#which}

サーバーは一つのプロセスです。構成ごとに変わるのは **どこで動くか** と **どのランタイムを駆動するか** ([デプロイバックエンド](/ja/reference/deploy-backends)) です。

| やりたいこと | 構成 | `cornus setup` のシナリオ |
| --- | --- | --- |
| 最小の準備で Cornus を試す | [ローカル、Docker](#local-docker) | `local` |
| Docker デーモンなしで動かす | [ローカル、containerd](#local-containerd) または [bare](#local-bare) | `local` |
| デーモンを一切使わない | [ローカル、bare](#local-bare) | `local` |
| Incus のインスタンスを使う | [ローカル、Incus](#local-incus) | `local` |
| 手元のマシンからクラスターへデプロイする | [ローカル、Kubernetes](#local-kubernetes) | `local` |
| もっと強力なビルド / デプロイ用ホストを使う | [SSH 経由のリモートホスト](#ssh) | `ssh-*` |
| デプロイ先のクラスター内で Cornus を動かす | [クラスター内](#in-cluster) | `kube-port-forward`、`kube-url` |
| ホストを汚さない | [コンテナとして](#in-a-container) | `docker-container` |
| 他の人が運用するサーバーを使う | [セットアップ不要](#existing) | `url` |

どの構成にも共通する原則が二つあります。

- **ビルドエンジンには権限が必要です。** runc、overlayfs、user 名前空間を使うため、サーバーを root / 特権付きで実行するか、`cornus serve --rootless` を使ってください。[権限のありかた](/ja/reference/deploy-backends#権限の考え方) を参照してください。
- **決める前に確認してください。** `cornus daemon preflight` は `cornus serve` が起動時に実行するのと同じホストチェックを行い、`cornus serve` が起動を拒否する構成では終了ステータスが 0 以外になります。以下の手順書はすべてこれを使います。

## ローカルサーバー {#local}

Cornus を手元のマシンで動かします。データディレクトリにはレジストリの CAS とビルドキャッシュが入ります。再起動をまたいで保持するには `--data-dir` (または `CORNUS_DATA`) を指定してください。

まずバイナリを入手します。[インストール](/ja/introduction/installation) を参照してください。

### Docker {#local-docker}

既定であり、準備が最も少ない構成です。

**必要なもの:** Docker ソケット `/var/run/docker.sock`。

```sh
cornus daemon preflight                     # 先にホストを検証する
cornus serve --data-dir ~/.local/share/cornus
```

**確認:** サーバーが起動していれば `cornus health` は何も出力せず終了ステータス 0 になります。

**詳細:** [`dockerhost` バックエンド](/ja/reference/deploy-backends#dockerhost-既定)。

### containerd {#local-containerd}

dockerd は不要ですが、デーモンは使います。

**必要なもの:** root、containerd ソケット、そして `/opt/cni/bin` の CNI プラグイン (`bridge`、`portmap`、`host-local`、`loopback`)。

```sh
sudo CORNUS_DEPLOY_BACKEND=containerd cornus daemon preflight
sudo CORNUS_DEPLOY_BACKEND=containerd cornus serve --data-dir /var/lib/cornus
```

**確認:** `cornus health`。

**詳細:** [`containerd` バックエンド](/ja/reference/deploy-backends#containerd)。

### bare (デーモンなし) {#local-bare}

Cornus が自分で OCI ランタイムを駆動し、自分で監督します。dockerd も containerd も使いません。

**必要なもの:** root、`PATH` 上の OCI ランタイム、そして同じ CNI プラグイン。既定は `runc` で、`CORNUS_BARE_RUNTIME` により `crun`、`youki`、`runsc` (gVisor) を選べます。

```sh
sudo CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight
sudo CORNUS_DEPLOY_BACKEND=bare cornus serve --data-dir /var/lib/cornus
```

ランタイムが見つからない場合は、最初のデプロイ時ではなく起動時に、対処のわかるエラーで直ちに失敗します。

::: warning この構成は systemd で動かしてください
`bare` では **cornus 自身がワークロードの監督者** です。各コンテナの PID 1 を待ち受け、再起動ポリシーを自分で適用します (`CORNUS_BARE_SHIM` を使えばコンテナごとの shim に切り離せますが、既定では無効です)。そのため端末から起動したサーバーは、終了するときにワークロードの監督ごと道連れにします。生き残ったコンテナに再接続し、ホスト再起動後に再構築する起動時の reconcile も、cornus が動いていなければ実行されません。クラッシュと再起動の両方をワークロードが越えられるようにするのが `Restart=on-failure` と `WantedBy=multi-user.target` です。

他のバックエンドは監督をデーモンやクラスターに任せているため、cornus を失っても失われるのは API であってワークロードではありません。そちらではフォアグラウンドの `cornus serve` も妥当な開発ループのままです。

`cornus setup` はこの構成向けの `cornus.service` を提案します。自分で組み立てるより、それを使ってください。
:::

**確認:** `cornus health`。

**詳細:** [`bare` バックエンド](/ja/reference/deploy-backends#bare)。

### Incus {#local-incus}

ワークロードは Incus のアプリケーションコンテナになります。

**必要なもの:** incusd **6.3+** (それ以前のリリースには OCI 対応がありません)、そのソケットへのアクセス権、そして **デーモンホスト側の** `skopeo` と `umoci`。incusd がイメージを平坦化するためにこれらを呼び出すので、incusd が動くホストに必要です。

```sh
CORNUS_DEPLOY_BACKEND=incus cornus daemon preflight
CORNUS_DEPLOY_BACKEND=incus cornus serve --data-dir ~/.local/share/cornus
```

`CORNUS_INCUS_SOCKET` (既定 `/var/lib/incus/unix.socket`) と `CORNUS_INCUS_PROJECT` (既定 `default`) で接続先を変更できます。

**確認:** `cornus health`。

**詳細:** [`incus` バックエンド](/ja/reference/deploy-backends#incus)。

### 手元のマシンから Kubernetes へ {#local-kubernetes}

サーバーはローカルで動き、kubeconfig で到達できるクラスター ([k3s](https://k3s.io/)、kind、minikube、リモートのもの) へデプロイします。ローカルのコンテナランタイムは一切使いません。

**必要なもの:** 到達できるクラスター (`KUBECONFIG`、なければ `~/.kube/config`) と、`CORNUS_K8S_NAMESPACE` (既定 `default`) で Deployment と Service を管理できる RBAC。

```sh
CORNUS_DEPLOY_BACKEND=kubernetes cornus daemon preflight
CORNUS_ADVERTISE_REGISTRY=192.0.2.10:5000 \
  CORNUS_DEPLOY_BACKEND=kubernetes cornus serve --data-dir ~/.local/share/cornus
```

::: warning イメージをプルするのはノードであって、あなたではありません
ここでは `CORNUS_ADVERTISE_REGISTRY` は任意ではありません。ビルドしたイメージをこのサーバーのレジストリからプルするのはクラスターのノード自身です。そのため `127.0.0.1:5000` のようなアドレスは *ノード上では* ノード自身を指してしまい、手元のマシンに置かれたイメージをプルできずにすべてのデプロイが失敗します。ノードが到達できるアドレスを設定してください。
:::

Cornus は本来クラスター **内部** で動かすことを主眼としており、その場合レジストリは構造上ノードが到達できるサービスエンドポイントになるので、この問題は起きません。サーバーを特にローカルで動かしたい理由がなければ [クラスター内](#in-cluster) を選んでください。

**詳細:** [`kubernetes` バックエンド](/ja/reference/deploy-backends#kubernetes-k8s)。

## SSH 経由のリモートホスト {#ssh}

サーバーは別のマシンで動き、CLI はローカルポートをバインドしない SSH トンネルで到達します。どのランタイムを駆動するかは **そちら側** で `CORNUS_DEPLOY_BACKEND` により決まります。トンネル自体はバックエンドに依存しません。

四つとも手順の形は同じです。

1. リモートホストに cornus バイナリをインストールする ([インストール](/ja/introduction/installation))。
2. そのバックエンドの前提条件を満たす (下記)。
3. 検証する: `ssh HOST '<env> cornus daemon preflight'`。
4. ループバックにバインドして実行する。トンネルはホスト側で出るので、ホスト自身のループバックでサーバーに届きます: `ssh HOST '<env> cornus serve --addr 127.0.0.1:5000'`。
5. 手元側を設定する: `cornus setup --scenario ssh-<backend>`。

手順 4 はシェルで済ませるより systemd ユニットにする価値があります。`cornus setup` は選んだバックエンド向けの正しい `cornus.service` を、前提条件をコメントに含めて生成します。自分で組み立てるより、それを使ってください。

### Docker {#ssh-docker}

**ホストに必要なもの:** Docker ソケット。**環境変数:** なし (既定)。

```sh
ssh HOST 'cornus daemon preflight'
cornus setup --scenario ssh-docker
```

### containerd {#ssh-containerd}

**ホストに必要なもの:** root、containerd ソケット、`/opt/cni/bin` の CNI プラグイン。**環境変数:** `CORNUS_DEPLOY_BACKEND=containerd`。

```sh
ssh HOST 'sudo CORNUS_DEPLOY_BACKEND=containerd cornus daemon preflight'
cornus setup --scenario ssh-containerd
```

### bare {#ssh-bare}

**ホストに必要なもの:** root、`PATH` 上の OCI ランタイム、CNI プラグイン。**環境変数:** `CORNUS_DEPLOY_BACKEND=bare`。

```sh
ssh HOST 'sudo CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight'
cornus setup --scenario ssh-bare
```

### Incus {#ssh-incus}

**ホストに必要なもの:** incusd 6.3+、ソケットへのアクセス権、`skopeo` と `umoci`。**環境変数:** `CORNUS_DEPLOY_BACKEND=incus`。

```sh
ssh HOST 'CORNUS_DEPLOY_BACKEND=incus cornus daemon preflight'
cornus setup --scenario ssh-incus
```

### そのホスト上でコンテナとして {#ssh-container}

リモートホストにバイナリをインストールする必要はありません。Docker ホストであれば、サーバーは公開イメージから実行でき、同じトンネルで到達できます。`cornus setup --scenario ssh-docker` は「サーバーをリモートホスト上でコンテナとして実行しますか」と尋ね、この形に切り替えます。

**ホストに必要なもの:** Docker と、データディレクトリ用のホスト側ディレクトリ。cornus バイナリも systemd ユニットも不要です。

```sh
# 先に、そちらでバインドを確認します。
ssh HOST 'docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight'

ssh HOST 'docker run -d --name cornus --privileged --restart unless-stopped \
  -p 127.0.0.1:5000:5000 \
  -e CORNUS_DATA=/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest serve --addr :5000'
```

公開先は **リモートホストのループバック** です。そこはまさに SSH トンネルが出る場所であり、そのホストのネットワークには何も公開されません。この形では systemd ユニットを提案しません。起動すべきバイナリがなく、再起動後に復帰させるのは `--restart unless-stopped` だからです。

バインドの重要性はローカルの場合と同じです。[コンテナとして](#in-a-container) と [サーバーをコンテナで実行する](/ja/guides/server-in-a-container) を参照してください。

**四つ共通のレジストリに関する注意:** ホストのデプロイ先が導出されたレジストリアドレスからプルできない場合は、`--registry-host` を設定してください。

**詳細:** [SSH 経由のリモートコンテナホスト](/ja/guides/remote-docker-hosts)。

## クラスター内 {#in-cluster}

Cornus が本来想定している構成です。サーバーはデプロイ先のクラスター内で StatefulSet として動くため、レジストリは構造上ノードが到達できるサービスエンドポイントになり、ビルドキャッシュも再起動をまたいで残ります。

**必要なもの:** クラスターと `kubectl` / `helm`。手元のマシンには CLI 以外は不要です。

```sh
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus
kubectl rollout status statefulset/cornus --timeout=300s
```

Helm が推奨経路です。chart にはバージョンがあり、イメージタグも chart のバージョンに追随するため、1 コマンドで互いに一致するサーバーとマニフェストが手に入ります。raw マニフェストでも構いませんが、ブランチではなく **リリースタグ** に固定してください。これは広範な RBAC を持つ特権付き StatefulSet をインストールします。

次に CLI をそこへ向けます。

```sh
cornus setup --scenario kube-port-forward   # 自動ポート転送。公開は不要
cornus setup --scenario kube-url            # または ingress URL で到達する
```

**レジストリの公開:** NodePort のレジストリはノードのアドレスを自動的に通知します。ClusterIP や ingress の場合は `registry.advertiseHost` (またはクライアント側の `--registry-host`) を設定してください。`cornus setup` が対応する `cornus-values.yaml` を生成します。

**詳細:** [インストール](/ja/introduction/installation)、[Helm chart の values](/ja/reference/helm-values)、[リモートクラスターで作業する](/ja/guides/remote-clusters)、そしてこの流れをシングルノードの k3s クラスターでたどる [クイックスタート](/ja/introduction/quick-start)。

## docker ホスト上のコンテナとして {#in-a-container}

サーバー自体を、管理対象の Docker ホスト上でコンテナとして動かします。ここでの難所はもっぱらバインドマウントであり、間違えても起動時には失敗しません。デプロイ時に何も言わずに失敗します。

**必要なもの:** Docker と、データディレクトリ用のホスト側ディレクトリ。それだけです。Compose も、配布されるファイルも必要ありません。

```sh
# 先に、実際に動かすイメージでバインドを確認します。
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight

docker run -d --name cornus --privileged --restart unless-stopped \
  -p 127.0.0.1:5000:5000 \
  -e CORNUS_DATA=/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest serve --addr :5000
```

preflight は **先に** 実行してください。何も動いていないうちにバインドを直すほうがはるかに安く済みます。三つのオプションはいずれも必要です。ソケットのバインドがあるからこそホストの docker を使えます。`:rshared` があるからこそ、コンテナ内で cornus が行ったマウントがホストへ伝わります。`--privileged` はインプロセスのビルドとカーネル 9P マウントに必要です。

`cornus setup --scenario docker-container` はホスト側のデータディレクトリとポートを尋ね、その回答を埋め込んだこのコマンドを表示します。

**確認:** `cornus health`。

**詳細:** [サーバーをコンテナで実行する](/ja/guides/server-in-a-container)。

## 他の人が運用するサーバー {#existing}

セットアップは不要です。運用している人に URL と、必要なら資格情報を尋ねてから、次を実行します。

```sh
cornus setup --scenario url
```

認証が必要な場合は、ウィザードが SSH 鍵の登録またはトークンの保存を案内します。[セキュリティと認証](/ja/guides/security) を参照してください。

## サーバーが起動したら {#after}

```sh
cornus health                # 待ち受けているか
cornus version               # 設定したプロファイルで CLI が到達できるか
cornus compose up            # 何かデプロイする
```

`cornus health` は成功するのに `cornus version` が失敗する場合、サーバーは問題なく接続プロファイルのほうに問題があります。[`cornus setup`](/ja/cli/setup) をもう一度実行するか、[接続設定](/ja/reference/connection-config) を参照してください。

**関連項目:** [cornus setup](/ja/cli/setup)、[デプロイバックエンド](/ja/reference/deploy-backends)、[cornus serve](/ja/cli/serve)、[セキュリティと認証](/ja/guides/security)。
