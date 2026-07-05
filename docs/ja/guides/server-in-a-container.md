# サーバーをコンテナで実行する

cornus サーバー自体を、**管理対象の docker または containerd ホスト** 上のコンテナとして実行します。これは Kubernetes におけるクラスター内 Helm インストールに対応する、ホストバックエンド向けの方法です。ホストにはコンテナ以外何もインストールせずに cornus を利用でき、ワークロードは引き続きホスト自身のランタイム上に配置されます。

これは [リモートの docker/containerd ホスト](/ja/guides/remote-docker-hosts) とは異なります。あちらではサーバーが SSH トンネルの向こう側で動作し、手元のマシンから接続します。ここではサーバーはランタイムと同じホストにあり、たまたまコンテナ化されているだけです。

## バインドが重要な理由

cornus がコンテナランタイムに渡すパスはすべて、そのランタイムによって **ホスト** のファイルシステム上で解決されます。cornus 自身のファイルシステム上ではありません。ホスト上で直接動作するサーバーがこれに気づくことはありません。両者が同一だからです。しかしコンテナ化されたサーバーでは、両者の対応関係を教える必要があります。さもなければ、相手側では何も意味しないパスを渡すことになります。

この失敗は表面に現れません。見つからないパスを渡されたランタイムは、そのパスを作成したうえでワークロードを起動します。マウントは空のディレクトリになり、失敗したコマンドはなく、どのログにも何も出ません。そのため cornus は推測を避けます。可能な場合は対応関係を検出し、それ以外の場合は事前にその旨を伝えます。

## Docker

```sh
docker run -d --name cornus \
  --privileged \
  -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest
```

- **ソケットのバインド** によって、これがホストの docker を対象とするものになります。これがなければ対象は存在しません。
- データディレクトリの **`:rshared`** は、コンテナ内で cornus が作成したマウントをホストへ到達させるためのものです。これがない場合、[クライアントローカルマウント](/ja/guides/deploying-workloads#クライアントローカルディレクトリをリモートワークロードにマウントする-local-mount、9p-でストリーム) (`--local-mount`) は理由の説明とともに拒否されます。それ以外はすべて動作します。
- **`--privileged`** はイメージをインプロセスでビルドする場合と、`--local-mount` のためのカーネル 9P マウントを行う場合に必要です。ビルド済みイメージをデプロイするだけのサーバーであれば外せます。

cornus は自身のコンテナについてデーモンに問い合わせることで `/srv/cornus` と `/var/lib/cornus` の対応関係を検出するため、追加の設定は不要です。

### ワークロードへ到達する

`cornus port-forward`、`cornus tunnel`、およびサーバーからの公開ポートへのアクセスは、ワークロードのコンテナ IP へダイヤルします。自身のブリッジネットワーク上にいるサーバーは、ユーザー定義ネットワーク上のワークロードへの経路を持たないため、ダイヤルはその理由の説明とともにタイムアウトします。対処は 2 通りあります。

- 上記の `docker run` に `--network host` を加え、サーバーがすべての docker ネットワークについてホストと同じ視点を持つようにする。
- `CORNUS_DOCKER_REMOTE=1` (および `CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL`) を設定し、インスタンスごとの companion 経由でワークロードへ到達する。これはレプリカごとに追加のコンテナを実行するため、他の理由で companion が必要でない限りホストネットワークを優先してください。

## containerd

::: warning
**containerd** ホスト上でのコンテナ化された cornus はまだ完成していません。パスの変換は動作しますが、バックエンドの CNI ネットワークは依然として cornus が動作しているネットワーク名前空間の内側にワークロードネットワークを構築するため、サーバーはホストの名前空間を共有する必要があります。またコンテナイメージには CNI プラグインのバイナリがまだ同梱されていません。両方が解決されるまでは、containerd バックエンドは [ホスト上で直接](/ja/guides/remote-docker-hosts) 実行してください。
:::

このモードに必要となる要件は次のとおりで、preflight はすでにこれらを報告します。

- `-v /run/containerd/containerd.sock:/run/containerd/containerd.sock`
- `-v /srv/cornus:/var/lib/cornus:rshared` — ここでは任意ではなく必須です。containerd バックエンドは **すべての** デプロイでデータディレクトリ配下のパスを使用するため (ボリュームの実体、管理対象の `/etc/hosts`、ログファイル)、ランタイムから見えないデータディレクトリの場合、サーバーは空のワークロードを作る代わりに起動を拒否します。
- CNI の配管のための `--network host`。
- コンテナ内の `/opt/cni/bin` または `CORNUS_CNI_BIN_DIR` 配下にある CNI プラグイン (`bridge`、`portmap`、`host-local`、`loopback`)。

## 確定する前に確認する

サーバーとして使う予定のイメージの中で、同じマウントと同じ環境で preflight を実行してください。`cornus serve` が起動時に行うものとまったく同じチェックを実行し、サーバーが起動を拒否する構成では非ゼロで終了します。

```sh
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight
```

```
cornus runs in a container (a1b2c3d4e5f6) on a docker host; translating its paths for the runtime
  [ok  ] data-dir-host-visible: data dir /var/lib/cornus is /srv/cornus on the host
  [warn] client-local-mounts: client-local mounts unavailable: ...
           remedy: run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded
```

`--output json` は同じ結果を 1 つのオブジェクトとして出力するため、CI のゲートに使えます。

実行中のサーバーも同じ結論を報告します。起動時のサマリー行と問題ごとの警告、そしてクライアントが `GET /.cornus/v1/info` から読み取る機能フラグです。これにより `cornus setup` の検証は、`--local-mount` を実現できないサーバーであることを、それに頼る前に伝えられます。

## 対応関係を自分で宣言する

コンテナのマウント内容をランタイムに問い合わせられない場合 (docker 以外のランタイムや、デーモンが報告しないコンテナ)、対応関係を明示的に宣言します。

```sh
-e CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus
```

複数の組はカンマ区切りで指定します。明示したエントリーは常に検出されたものより優先されるため、誤った検出結果を訂正する手段でもあります。不正な値は、黙って無視される設定ではなく起動時エラーになります。

## 変換されないもの

デプロイスペックや Compose ファイルに **あなたが** 書いたバインドマウントは、すでにホストのパスです。開くのはデーモンだからです。したがってコンテナ化されていないサーバーの場合とまったく同様に、そのまま渡されます。`CORNUS_ALLOW_BIND_SOURCES` も同じで、これらの接頭辞はホストから見たとおりに書いてください。

変換されるのは、cornus 自身がデータディレクトリ配下に用意したパスだけです。

## Docker-in-Docker についての注記

駆動対象のデーモンと **同じコンテナ内** でコンテナ化された cornus は (Docker-in-Docker のテストハーネスのように)、そのデーモンのマウント名前空間を共有するため、パスはすでに一致しており何も変換されません。cornus は、cornus が動作しているコンテナをそのランタイムが作成したと確認できるかどうかで上記の場合と区別するため、この 2 つの形態で設定を変える必要はありません。
