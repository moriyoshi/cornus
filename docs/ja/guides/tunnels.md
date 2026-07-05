# トンネル

**パブリックトンネル** (`cornus tunnel`) は、ホスト型の中継経由でワークロードポートをパブリックインターネットに公開します。クラスターネイティブな Ingress リソースは不要で、経路上に公開ポートも必要ありません。進行中の作業を共有したり、Webhook を受けたり、スマートフォンでテストしたりする用途に使えます。ホスト型の中継ではなく実際の Ingress リソースによる永続的なホスト名が必要な場合は、[イングレス](/ja/guides/ingress)を参照してください。

## 仕組み

`cornus tunnel <name> <port>` はワークロードポート用の **パブリック HTTPS URL** を返し、`Ctrl-C` まで起動し続けます。Cornus **サーバー** がトンネルをホストし、ポート転送と同じバイトブリッジ経由で各受信接続をワークロードへ中継します。そのため、Docker ホスト、containerd、Kubernetes の任意のバックエンドで、ワークロードが公開していないポートにも到達できます。

```sh
cornus tunnel [--authtoken TOKEN | --authtoken-file FILE] [--proto http|tcp] <name> <port>
```

```sh
cornus tunnel --server http://cornus.example:5000 \
  --authtoken "$NGROK_AUTHTOKEN" web 80
```

トンネル資格情報は、すでに認証済みの要求にクライアントが注入します。サーバーは事前にそれを知りません。運用者は代わりにサーバー側の既定資格情報として `CORNUS_TUNNEL_AUTHTOKEN` を設定できます。その場合、呼び出し元は `--authtoken` を省略できます。HTTP または生の TCP は `--proto http` / `--proto tcp` で選びます。完全なフラグセットは [`cornus tunnel`](/ja/cli/tunnel) を参照してください。

## バックエンド

トンネルバックエンドはサーバー側で `CORNUS_TUNNEL_BACKEND` により選択します (既定は `ngrok`)。具体的なバックエンドは差し替え可能で、選択されたものだけが有効になります。4 つのバックエンドはすべて同じクライアントコマンドを共有し、サーバー側の `CORNUS_TUNNEL_BACKEND` とバックエンドごとの環境変数だけが変わります。

| バックエンド | 注入する資格情報 | 備考 |
| --- | --- | --- |
| `ngrok` (既定) | ngrok の authtoken (`NGROK_AUTHTOKEN`) | プロセス内の ngrok エージェント。子プロセスなし |
| `ssh` | SSH 秘密鍵 (PEM)、パスワード、または転送された ssh-agent (`--forward-agent`) | 自前でホスト可能なトンネルサーバー (sish、serveo、pinggy、localhost.run、GatewayPorts 付きプレーン `sshd`) への SSH リモート転送。バイナリ内の SSH スタックを再利用 |
| `cloudflare` | なし (匿名) | `cloudflared` バイナリ (`CORNUS_TUNNEL_CLOUDFLARED_BIN`) 経由の Cloudflare クイックトンネル |
| `tailscale` | なし | `tailscale` バイナリ経由の Tailscale Funnel。ノードは帯域外で tailnet に参加するため、ノードごとに Funnel は 1 つ |

`ssh` バックエンドでは、エンドポイントを `CORNUS_TUNNEL_SSH_ADDR` / `CORNUS_TUNNEL_SSH_USER` で設定し、host-key verification を `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` または `CORNUS_TUNNEL_SSH_HOSTKEY` で設定します (fail-closed。開発のみでは `CORNUS_TUNNEL_SSH_INSECURE=1`)。環境変数の完全な一覧は [サーバー環境変数](/ja/reference/server-env-vars) にあります。

## 資格情報を安全に渡す

`--authtoken TOKEN` は secret を直接 argv に置くため、マシン上の他のユーザーが `ps` で読み取れるほか、シェルが履歴に書き込むこともよくあります — 手早いローカルテスト以外では避けてください。優先順位は次のとおりです: 資格情報を一切渡さない (サーバーに既定値がある場合。後述)、次にバックエンドの環境変数 (kong がこれを `--authtoken` へ自動的に読み込むので、値がコマンドライン引数として現れません)、最後に `--authtoken-file FILE` (ファイルから secret を読み込み、argv にも履歴にも残しません)。以下のレシピでは一貫して環境変数 / ファイル形式を使います。

## ngrok (既定) でワークロードを公開する

既定のバックエンドです。追加のバイナリのインストールも、authtoken 以外のサーバー側ネットワーク設定も不要です。

1. [ngrok.com](https://ngrok.com) にサインインし、ダッシュボードの「Your Authtoken」ページから authtoken をコピーします。
2. トークンは呼び出しごとにクライアント側で渡すか、サーバー側の既定値として設定します。
   ```sh
   # クライアント側、呼び出しごと: 一度 export すれば自動的に読み込まれます —
   # これを設定していれば、コマンドラインで --authtoken を指定する必要はありません。
   export CORNUS_TUNNEL_AUTHTOKEN=2ab3...
   cornus tunnel web 80
   ```
   あるいは *同じ変数名* をサーバー側の既定値として設定すれば (systemd ユニット、コンテナの環境変数、Helm の `values.yaml` など、サーバープロセスが環境変数を受け取る場所ならどこでも)、クライアントは資格情報を完全に省略できます — 同じ名前を 2 つの異なるプロセスの環境で使うのであって、1 つの値を共有しているわけではありません。
   ```
   CORNUS_TUNNEL_AUTHTOKEN=2ab3...
   ```
   クライアント側では `NGROK_AUTHTOKEN` も legacy alias として引き続き使えます。
3. cornus はパブリックな `https://<random>.ngrok-free.app` URL を表示し、`Ctrl-C` まで動作をブロックします。`Ctrl-C` でトンネルを終了します。

- `CORNUS_TUNNEL_BACKEND` はすでに既定で `ngrok` なので、サーバー側でのバックエンド選択は不要です。
- ngrok エージェントはサーバー上でプロセス内実行されるため、インストールするものはありません。
- 無料の ngrok アカウントでは、実行のたびに新しいランダムなサブドメインが割り当てられます。有料プランでは固定のサブドメインを使えます。

**関連項目:** [cornus tunnel](/ja/cli/tunnel)

## SSH リバース転送でワークロードを公開する

cornus のバイナリ内 SSH スタックを、SSH リモート転送 (`ssh -R`) を受け付ける任意のエンドポイントに対して再利用します。自前でホストするリレー (sish、`GatewayPorts yes` を設定したプレーンな `sshd`) でも、公開されているもの (serveo.net、pinggy.io、localhost.run) でも構いません。

1. `ssh -R` を受け付ける SSH トンネルエンドポイントを選ぶか、自前で用意します。
2. サーバーの環境変数 (systemd ユニット、コンテナの環境変数、Helm の `values.yaml`) に次を設定して、そのエンドポイントに向けます。
   ```
   CORNUS_TUNNEL_BACKEND=ssh
   CORNUS_TUNNEL_SSH_ADDR=tunnel.example.com:22
   CORNUS_TUNNEL_SSH_USER=cornus
   ```
   `CORNUS_TUNNEL_SSH_USER` は未設定なら既定で `cornus` になります。`CORNUS_TUNNEL_SSH_BIND` は既定で `0.0.0.0:0` です (リモート側にポートを選ばせます)。
3. host-key verification を設定します。このバックエンドは fail-closed のため、次のいずれかが必須です。
   ```sh
   CORNUS_TUNNEL_SSH_KNOWN_HOSTS=/etc/cornus/known_hosts
   # または鍵を 1 つだけ固定する場合:
   CORNUS_TUNNEL_SSH_HOSTKEY="ssh-ed25519 AAAA... tunnel.example.com"
   # 開発時のみ、検証を完全にスキップする場合:
   CORNUS_TUNNEL_SSH_INSECURE=1
   ```
4. cornus がパブリック URL を導出する方法を指定します。リレーが SSH セッションのバナーに自身の URL を表示する場合 (sish、serveo、pinggy がこれに該当します)、それを自動的に取得できます。
   ```sh
   CORNUS_TUNNEL_SSH_URL_FROM_SESSION=1
   ```
   そうでない場合は、割り当てられたリモートポートからテンプレートで URL を組み立てます。
   ```sh
   CORNUS_TUNNEL_SSH_URL_TEMPLATE='https://{port}.tunnel.example.com'
   ```
5. SSH の資格情報を、次の 2 つの方法のいずれかで渡します。

   - **サーバー側で共有する ID** — 暗号化されていない秘密鍵の PEM かパスワードのうち、リレーが受け付ける方です。リレーとの SSH ハンドシェイクは **サーバー** 側で行われクライアント側ではないため、これは通常、呼び出し元ごとの資格情報ではなく cornus サーバー全体で共有される 1 つのサービス ID です。そのため一度だけサーバー側の既定値として設定し、クライアントは資格情報を完全に省略できるようにします。
     ```
     CORNUS_TUNNEL_AUTHTOKEN=<PEM の内容、またはパスワード>
     ```
     本当に呼び出し元ごとの資格情報が必要な場合は、argv に置く代わりにクライアント側でファイルから読み込みます。
     ```sh
     cornus tunnel --authtoken-file ~/.ssh/id_ed25519 web 80
     ```
   - **転送された ssh-agent** — 鍵の実体はクライアントから一切出ません。サーバーの SSH ハンドシェイクは、代わりに呼び出し元のローカルな `ssh-agent` にチャレンジへの署名を依頼します。
     ```sh
     cornus tunnel --forward-agent web 80
     ```
     パスフレーズで保護された鍵で認証できる唯一の方法です。復号済みの鍵を保持しているのは cornus ではなくエージェントだからです。`ssh -A` と同様に、`--forward-agent` は信頼できる cornus サーバーに対してのみ使ってください。トンネルの起動中、サーバーはリレー由来のものに限らず任意のチャレンジへの署名を、転送されたエージェントに依頼できます。cornus がエージェントに問い合わせるのは SSH ハンドシェイクの間だけで、トンネルの存続期間全体ではありません。

- `--authtoken` として直接渡すパスフレーズ付きの秘密鍵はサポートされません。その場合は `--forward-agent` を使うか、暗号化されていない鍵かパスワードにフォールバックしてください。
- known-hosts ファイルもなく、固定した host key もなく、insecure の opt-in もない場合、未検証のホストを信頼せずに接続が拒否されます。

**関連項目:** [cornus tunnel](/ja/cli/tunnel)、[サーバー環境変数](/ja/reference/server-env-vars)

## Cloudflare Tunnel でワークロードを公開する

匿名の Cloudflare「クイックトンネル」です。Cloudflare アカウント、API トークン、DNS ゾーンのいずれも不要です。このバックエンドは `cloudflared` バイナリを呼び出しますが、公開されている cornus イメージにはこれが同梱されていません。サーバーをコンテナとして動かす場合は、それを上に重ねたカスタムイメージをビルドしてください。

```dockerfile
FROM ghcr.io/moriyoshi/cornus:latest
RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && curl -fsSL -o /usr/local/bin/cloudflared \
         https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
    && chmod +x /usr/local/bin/cloudflared \
    && apt-get purge -y curl && rm -rf /var/lib/apt/lists/*
```

そのイメージを標準イメージの代わりにデプロイします (Helm の `image.repository` / `image.tag` の値、または k8s マニフェストを更新します)。サーバーをコンテナ化せずホスト上で直接動かす場合は、そのホストに `cloudflared` をインストールするだけで済みます。

1. `cloudflared` をサーバーホストにインストールします — 上記のカスタムイメージ経由で、あるいはサーバーがコンテナ化されていない場合は直接ホストに。
2. `PATH` 上にない場合は、サーバーの環境変数で cornus にその場所を伝えます。
   ```
   CORNUS_TUNNEL_CLOUDFLARED_BIN=/usr/local/bin/cloudflared
   ```
3. サーバー側でバックエンドを選択します。
   ```
   CORNUS_TUNNEL_BACKEND=cloudflare
   ```
4. トンネルを起動します。バックエンドは匿名なので `--authtoken` は不要です。
   ```sh
   cornus tunnel web 80
   ```
5. cornus は `https://<random-words>.trycloudflare.com` という URL を表示します。

- クイックトンネルは一時的なもので、実行のたびにホスト名が変わります。自分のドメインに固定したホスト名を持つ named tunnel (Cloudflare アカウントトークンによるもの) はまだサポートされていません。

**関連項目:** [cornus tunnel](/ja/cli/tunnel)

## Tailscale Funnel でワークロードを公開する

すでに tailnet に参加しているノードを通じて公開します。cornus が管理する資格情報は一切不要です。ノードが tailnet に参加していること自体が認可になります。このバックエンドは `tailscale` バイナリを呼び出しますが、公開されている cornus イメージにはこれが同梱されていません。`sudo tailscale up` は長期稼働するホスト向けの対話的なコマンドであり、ephemeral な pod に対して手動で実行できるものではないため、2 つのデプロイ形態にはそれぞれ異なるセットアップが必要です。

### Kubernetes 上で、Helm chart 経由の場合

chart は、無人で tailnet に参加する `tailscaled` をサイドカーとして動かし、`tailscale` CLI バイナリを cornus コンテナと共有できます — カスタムイメージも、手動での `tailscale up` も不要です。

1. Tailscale 管理コンソールで tailnet 認証キーを作成します — **再利用可能** で、できれば **ephemeral** タグ付きにしてください。サイドカーの状態は pod 再起動をまたいで永続化されず、ephemeral なノードは切断時に tailnet に蓄積されず自動的に登録解除されるためです。
   ```sh
   kubectl create secret generic cornus-tailscale-authkey \
     --from-literal=authkey=tskey-auth-...
   ```
2. Tailscale 管理コンソールで、tailnet の HTTPS 証明書を有効にし (**DNS → Enable HTTPS**)、tailnet の ACL ポリシーでそのノードに Funnel 属性を付与します (`funnel` 属性を持つ `nodeAttrs` エントリ。正確な ACL の記述は Tailscale の Funnel ドキュメントを参照してください)。
3. `values.yaml` (または `--set`) でサイドカーを有効化します。
   ```yaml
   tailscale:
     enabled: true
     authKeySecret: cornus-tailscale-authkey
   ```
   これにより cornus コンテナに `CORNUS_TUNNEL_BACKEND`、`CORNUS_TUNNEL_TAILSCALE_BIN`、`TS_SOCKET` が自動的に設定されます — 利用可能な全設定 (hostname、image、追加の `tailscale up` 引数) は chart の `values.yaml` の "tailscale" ブロックを参照してください。
4. トンネルを起動します。`--authtoken` は不要です。
   ```sh
   cornus tunnel web 80
   ```
5. cornus はそのノードのパブリック URL `https://<node>.ts.net/` を表示します。

### それ以外の環境: プレーンなホスト、または Helm chart の外のコンテナ

このバックエンドは `tailscale` バイナリを呼び出しますが、公開されている `ghcr.io/moriyoshi/cornus:latest` イメージにはこれが同梱されていません。サーバーが Helm chart の外でコンテナとして動く場合 (素の `docker run`、手書きの k8s マニフェスト、`docker compose`)、それを上に重ねたカスタムイメージをビルドしてください。

```dockerfile
FROM ghcr.io/moriyoshi/cornus:latest
RUN apt-get update && apt-get install -y --no-install-recommends curl gnupg \
    && curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg \
         -o /usr/share/keyrings/tailscale-archive-keyring.gpg \
    && curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list \
         -o /etc/apt/sources.list.d/tailscale.list \
    && apt-get update && apt-get install -y --no-install-recommends tailscale \
    && apt-get purge -y curl gnupg && rm -rf /var/lib/apt/lists/*
```

`tailscaled` をその隣で動かし (Pod / ホストのネットワーク名前空間を共有するサイドカーコンテナとして、あるいは同じコンテナ内の別プロセスとして)、そのカスタムイメージを標準イメージの代わりにデプロイします。サーバーをプレーンなホスト上で直接動かす場合は、カスタムイメージは不要で、そのホストに Tailscale をインストールするだけで済みます。

1. Tailscale をインストールします — 上記のカスタムイメージ経由で、あるいはサーバーがコンテナ化されていない場合は直接ホストに — そして tailnet に参加させます。
   ```sh
   sudo tailscale up
   ```
2. 上記と同じ Tailscale 管理コンソールの手順 (HTTPS 証明書の有効化、Funnel 属性の付与) に従います。
3. `tailscale` が `PATH` 上にない場合は、サーバーの環境変数で cornus にその場所を伝えます。
   ```
   CORNUS_TUNNEL_TAILSCALE_BIN=/usr/bin/tailscale
   ```
4. サーバー側でバックエンドを選択します。
   ```
   CORNUS_TUNNEL_BACKEND=tailscale
   ```
5. トンネルを起動します。`--authtoken` は不要です。
   ```sh
   cornus tunnel web 80
   ```
6. cornus はそのノードのパブリック URL `https://<node>.ts.net/` を表示します。

- 1 つのノードは同時に 1 つの Funnel しか port 443 で提供できないため、同じサーバーホスト上での複数トンネルは競合します。これは cornus の制約ではなく、Tailscale Funnel の制約です。
- この URL は既定でインターネット上の誰からでも到達可能です。それを望まない場合は Tailscale の ACL でアクセスを制限してください。

**関連項目:** [cornus tunnel](/ja/cli/tunnel)、[Helm chart values](/ja/reference/helm-values)
