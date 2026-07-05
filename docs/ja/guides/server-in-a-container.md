# サーバーをコンテナで実行する

cornus サーバー自体を、**管理対象のホストランタイム** 上のコンテナとして実行します。これは Kubernetes におけるクラスター内 Helm インストールに対応する、ホストバックエンド向けの方法です。ホストにはコンテナ以外何もインストールせずに cornus を利用でき、ワークロードは引き続きホスト自身のランタイム上に配置されます。

どこまで問題なく動くかはバックエンドによって異なり、その差は見かけだけのものではありません。docker と containerd はどちらも自動で設定されます。いずれも、これからデプロイに使うランタイムに対して「自分はどのコンテナで動いているのか」を問い合わせ、その答えから自身のホストパスを導き出します。bare には問い合わせ先のデーモンがなく、そもそもパスの変換も不要です。incus はランタイムに cornus 自身のパスを一切渡しません。この 2 つで問われるのは、サーバーがワークロードへ到達できるかどうかだけです。以下、バックエンドごとに説明します。

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
- データディレクトリの **`:rshared`** は、コンテナ内で cornus が作成したマウントをホストへ到達させるためのものです。これがない場合、[クライアントローカルマウント](/ja/guides/deploying-workloads#クライアントローカルディレクトリをリモートワークロードにマウントする-local-mount、9p-でストリーム) (`--local-mount`) は理由の説明とともに拒否されます。それ以外はすべて動作します。
- **`--privileged`** はイメージをインプロセスでビルドする場合と、`--local-mount` のためのカーネル 9P マウントを行う場合に必要です。ビルド済みイメージをデプロイするだけのサーバーであれば外せます。

cornus は自身のコンテナについてデーモンに問い合わせることで `/srv/cornus` と `/var/lib/cornus` の対応関係を検出するため、追加の設定は不要です。

### ワークロードへ到達する

`cornus port-forward`、`cornus tunnel`、およびサーバーからの公開ポートへのアクセスは、ワークロードのコンテナ IP へダイヤルします。docker は異なるブリッジネットワーク間の通信を遮断するため、コンテナ化されたサーバーは `networks:` を宣言したワークロードへの経路を持たず、逆方向の経路も持ちません。

cornus はこれを自身で処理します。ユーザー定義ネットワークへデプロイする際、まず **自身のコンテナ** をそのネットワークへ接続し、デプロイ (およびネットワーク) が削除される際に切り離します。設定は不要です。ユーザー定義ネットワークへの接続はサーバー自身のデフォルトルートを変更しないため、外向きの接続性はそのまま保たれます。

サーバーがワークロードのネットワークのメンバーになることで、docker の組み込み DNS はそのネットワーク上でサーバーをコンテナ名によって解決できます。したがって caretaker companion やワークロードのテレメトリーがダイヤルバックする `CORNUS_ADVERTISE_URL` に、サーバーのコンテナ名 (`ws://cornus:5000`) を指定できます。

知っておく価値のある副次的な影響が 2 つあります。

- **サーバーはそれぞれのネットワークのアドレスプールを 1 つ消費します。** ネットワークの `ipam.config` の subnet や `ip_range` をレプリカ数ちょうどに設定している場合、1 つ足りなくなり、あふれたレプリカはデーモンの "no available IPv4 addresses on this network's address pools" で起動に失敗します。1 つ分広げてください。cornus 自身がその追加の利用者である場合、エラーにその説明を付け加えます。
- **サーバーのコンテナを作り直すと、その接続は失われます。** docker のエンドポイントはコンテナに属するため、`docker restart` では残りますが、アップグレード時の `docker rm` と `docker run` では残りません。cornus は次にワークロードへ到達する必要が生じた時点で必要に応じて再接続するため、これは自動的に回復します。アップグレード後にネットワークごとに 1 行、`attached this cornus server's own container to a running workload's network` というログが出ることがあります。

1 つだけ利用者側に委ねられているケースがあります。`networks:` をまったく持たないワークロードはデフォルトブリッジ上に配置されますが、cornus はそこへ自動的には接続しません。デフォルトブリッジへの接続はコンテナのデフォルトルートを **変更してしまう** ため、サーバー自身の外向き経路を黙って張り替えることは、それが直す障害よりも悪いからです。サーバーのコンテナもデフォルトブリッジ上にない場合、`port-forward` は経路がないことを報告し、2 通りの対処を示します。

- 上記の `docker run` に `--network host` を加え、サーバーがすべての docker ネットワークについてホストと同じ視点を持つようにする。
- `CORNUS_DOCKER_REMOTE=1` (および `CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL`) を設定し、インスタンスごとの companion 経由でワークロードへ到達する。これはレプリカごとに追加のコンテナを実行するため、他の理由で companion が必要でない限りホストネットワークを優先してください。

## containerd

```sh
ctr run -d --net-host --privileged \
  --mount type=bind,src=/run/containerd/containerd.sock,dst=/run/containerd/containerd.sock,options=rbind:rw \
  --mount type=bind,src=/srv/cornus,dst=/var/lib/cornus,options=rbind:rw \
  --mount type=bind,src=/run/cornus,dst=/run/cornus,options=rbind:rw \
  --env CORNUS_DATA=/var/lib/cornus \
  --env CORNUS_DEPLOY_BACKEND=containerd \
  ghcr.io/moriyoshi/cornus:latest cornus \
  cornus serve --addr :5000
```

`docker` ではなく `ctr` (または `nerdctl`) で起動しています。これは好みの問題ではありません。cornus は自身のマウントとネットワークモードを、これからデプロイに使う containerd に「自分はどのコンテナで動いているのか」と問い合わせて把握します。つまり **その同じ containerd がサーバーのコンテナを作成した場合にのみ** 自分自身を見つけられます。containerd ホスト上で docker を使ってサーバーを起動すると、そのコンテナは docker 自身の containerd (別のソケット) に属することになり、問い合わせは何も見つけられません。その場合 cornus はそう報告し、対応関係は `CORNUS_HOST_PATH_MAP` で指定します。

ソケットをバインドしていれば、パスに関する設定は不要です。データディレクトリのホスト側の綴りは、docker の場合とまったく同じように自動検出されます。

上記 4 つのバインドにはそれぞれ理由があり、そのうち 2 つは事前チェックがなければ無言で失敗します。

- **containerd ソケット** は、そもそも自己問い合わせを可能にするものです。これがないと cornus は自分がどのコンテナかを知ることができず、自身のパスがすでに containerd と一致しているものとみなして、コンテナ内部だけで通用する綴りを渡してしまいます。containerd はそれぞれをホスト上に空のまま新規作成します。中身のないボリューム、管理対象の `/etc/hosts` の不在、そして staging されないログ shim — その結果、正常に動作しているコンテナに対して `cornus logs` が何も返さなくなります。
- **`/srv/cornus:/var/lib/cornus`** はここでは任意ではなく必須です。このバックエンドは **すべての** デプロイでデータディレクトリ配下のパスを使用するため (ボリュームの実体、管理対象の `/etc/hosts`、ログファイル)、ランタイムから見えないデータディレクトリの場合、サーバーは空のワークロードを作る代わりに起動を拒否します。
- **`/run/cornus`** は、cornus が各インスタンスのネットワーク名前空間を pin し、そのパスを containerd に渡す場所です。containerd の shim はそのパスを *ホスト* のマウント名前空間で開き直します。`/run` はコンテナ固有なので、このバインドがないとすべてのデプロイが失敗します。しかもその失敗は遅く、イメージのプルの後、そして稼働していた既存のデプロイメントを削除した後に起こります。事前チェックはこれを `netns-host-visible` として報告し、起動を拒否します。
- **`--net-host`** は、CNI のブリッジとポート公開の NAT ルールを、サーバーのコンテナ内部ではなくホスト上に作らせます。これがないとデプロイは **成功** し、ポートは公開済みと報告されるのに、ホストからはそのポート上に何も見えません。そうなることを防ぐため、事前チェックは起動を拒否します。それでも構わない場合 — サーバー自身の `port-forward` と `tunnel` はいずれの場合もワークロードに到達します — `CORNUS_HOST_NETWORK=0` を設定すると、それを承知したものとして警告付きで起動します。

公開イメージには CNI プラグイン (`bridge`、`portmap`、`host-local`、`loopback`) が `/opt/cni/bin` に、そしてそれらが呼び出す `iptables` も同梱されているため、追加のインストールは不要です。自分で用意したものを使いたい場合は `CORNUS_CNI_BIN_DIR` で別の場所を指定します。

## bare

`bare` は containerd と CNI ネットワークを共有します。cornus が同じプラグインを fork するため、ブリッジとポート公開の NAT ルールは cornus 自身のネットワーク名前空間に作られます。しかし containerd の節のそれ以外の内容は当てはまりません。`bare` はデーモンを持たないためバインドすべきソケットがなく、その OCI ランタイムは cornus 自身の子プロセスなので cornus のマウント名前空間を共有し、パスが食い違うことはありません。データディレクトリも netns ディレクトリも、どちらもバインドは不要です。

残るのは公開ポートという 1 つの影響だけです。サーバーのコンテナを `--network host` で実行すれば、ホスト上で動かした場合と同じ挙動になります。これがない場合、`ports:` はサーバーのコンテナ内部で実現され、ホストからはそこに何も見えません。一方でサーバー自身の `port-forward` と `tunnel` は動作し続けます。どちらの状態なのかを cornus は検出できないため (問い合わせ先のデーモンがありません) 判別できないという警告を出します。`CORNUS_HOST_NETWORK=1` または `=0` でどちらかを明示できます。

## incus

Incus のインスタンスは incusd によって、ホストのネットワーク名前空間にある incusd 自身のブリッジ上にネットワーク接続されます。サーバーがどこに置かれているかで、そこへ到達できるかどうかが決まります。サポートされる答えは 2 つあります。

**incus のインスタンスとして** 同じデーモン上で動かすのが、経路の問題がまったく生じない方法であり、2 つ目のコンテナランタイムを必要としない方法でもあります。サーバーはワークロードと並んで incusd のブリッジ上に置かれるため、ホストネットワークも companion もなしにワークロードへ到達できます。cornus はこれを自分で認識します。自身のインスタンスを特定し、事前チェックは `workload-routing` をそのインスタンス名とともに ok として報告します。

唯一注意すべきなのは、デーモンのソケットをどう公開するかです。disk デバイスではなく **proxy** デバイスを使ってください。disk デバイスは非特権インスタンスの内側では `nobody` に id 変換され、cornus は接続できません。さらに、公開先はイメージ内に親ディレクトリが既に存在するパスにしてください。listener はインスタンスの起動時に作成されますが、`/var/lib/incus` はほとんどのイメージに存在せず、`bind: no such file or directory` で失敗します。

```sh
incus config device add cornus incusd proxy \
  listen=unix:/tmp/incus-daemon.sock \
  connect=unix:/var/lib/incus/unix.socket \
  bind=instance mode=0660
incus config set cornus environment.CORNUS_INCUS_SOCKET=/tmp/incus-daemon.sock
```

`connect` はホスト側のソケット、`listen` はインスタンス内での見え方であり、`CORNUS_INCUS_SOCKET` はその後者を cornus に指し示します。

それ以外にバインドするものはありません。このバックエンドは incusd に cornus 自身のパスを一切渡さないため、変換するものも、ホストから見えるようにすべきデータディレクトリもありません。

**incusd と並べて** 同じホスト上のコンテナで動かす方法も使えます。必要なのはデーモンのソケットとホストネットワークだけです。コンテナランタイムは何でも構いません。以下のフラグは `podman run` でも同じです。

```sh
docker run -d --name cornus --network host \
  -p 5000:5000 \
  -v /var/lib/incus/unix.socket:/var/lib/incus/unix.socket \
  -e CORNUS_DEPLOY_BACKEND=incus \
  ghcr.io/moriyoshi/cornus:latest
```

incus のブリッジへの経路を与えるのが `--network host` です。これがないとサーバーは経路を持たず、獲得する手段もありません。上記の docker における自己アタッチに相当するものは存在しません。cornus のコンテナは Incus のインスタンスではないからです。その結果 `port-forward`、`tunnel`、caretaker からの接続し直しがワークロードに到達できません。事前チェックはこれを `workload-routing` として報告し、失敗したダイヤルも同じ原因を名指しするので、素のタイムアウトを渡されることはありません。デプロイ自体には影響がなく、`ports:` の指定は引き続きホスト上で公開されます。incus はこれを、cornus ではなくデーモンの名前空間で listen する proxy デバイスで実現するためです。

どちらも適さない場合は、`CORNUS_INCUS_REMOTE=1` (および `CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL`) を設定すると、インスタンスごとの companion 経由で各インスタンスへ到達します。これはレプリカごとに追加のインスタンスを実行するため、他の理由で companion が必要でない限り、上記 2 つのいずれかを優先してください。

いずれも設定しない場合、ダイヤルは失敗しますが、cornus は素のタイムアウトを返すのではなく、その原因を名指しします。

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

コンテナのマウント内容をランタイムに問い合わせられない場合 (自己問い合わせの仕組みを持たないランタイム (`bare`) や、デーモンが報告しないコンテナ — 例えば containerd ホスト上で docker が起動したサーバー)、対応関係を明示的に宣言します。

```sh
-e CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus
```

複数の組はカンマ区切りで指定します。明示したエントリーは常に検出されたものより優先されるため、誤った検出結果を訂正する手段でもあります。不正な値は、黙って無視される設定ではなく起動時エラーになります。

## 変換されないもの

デプロイスペックや Compose ファイルに **あなたが** 書いたバインドマウントは、すでにホストのパスです。開くのはデーモンだからです。したがってコンテナ化されていないサーバーの場合とまったく同様に、そのまま渡されます。`CORNUS_ALLOW_BIND_SOURCES` も同じで、これらの接頭辞はホストから見たとおりに書いてください。

変換されるのは、cornus 自身がデータディレクトリ配下に用意したパスだけです。

## Docker-in-Docker についての注記

駆動対象のデーモンと **同じコンテナ内** でコンテナ化された cornus は (Docker-in-Docker のテストハーネスのように)、そのデーモンのマウント名前空間を共有するため、パスはすでに一致しており何も変換されません。cornus は、cornus が動作しているコンテナをそのランタイムが作成したと確認できるかどうかで上記の場合と区別するため、この 2 つの形態で設定を変える必要はありません。
