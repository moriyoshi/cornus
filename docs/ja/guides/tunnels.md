# トンネル

[`cornus tunnel`](/ja/cli/tunnel) を使ってワークロードポートを公開するための、バックエンドごとの段階的な手順です。仕組み — 資格情報の注入フローや、リスナー型とアップストリーム型というプロバイダーの形の違い — については [パブリックトンネル](/ja/topics/tunnels) を参照してください。4 つのバックエンドはすべて同じクライアントコマンドを共有し、サーバー側の `CORNUS_TUNNEL_BACKEND` とバックエンドごとの環境変数だけが変わります。

```sh
cornus tunnel [--authtoken TOKEN | --authtoken-file FILE] [--proto http|tcp] <name> <port>
```

**資格情報を安全に渡す。** `--authtoken TOKEN` は secret を直接 argv に置くため、マシン上の他のユーザーが `ps` で読み取れるほか、シェルが履歴に書き込むこともよくあります — 手早いローカルテスト以外では避けてください。優先順位は次のとおりです: 資格情報を一切渡さない (サーバーに既定値がある場合。後述)、次にバックエンドの環境変数 (kong がこれを `--authtoken` へ自動的に読み込むので、値がコマンドライン引数として現れません)、最後に `--authtoken-file FILE` (ファイルから secret を読み込み、argv にも履歴にも残しません)。以下のレシピでは一貫して環境変数 / ファイル形式を使います。

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

**関連項目:** [cornus tunnel](/ja/cli/tunnel)、[パブリックトンネル](/ja/topics/tunnels)

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
5. SSH の資格情報を渡します。リレーが受け付ける形式に応じて、暗号化されていない秘密鍵の PEM かパスワードのどちらかです。リレーとの SSH ハンドシェイクは **サーバー** 側で行われクライアント側ではないため、これは通常、呼び出し元ごとの資格情報ではなく cornus サーバー全体で共有される 1 つのサービス ID です。そのため一度だけサーバー側の既定値として設定し、クライアントは資格情報を完全に省略できるようにします。
   ```
   CORNUS_TUNNEL_AUTHTOKEN=<PEM の内容、またはパスワード>
   ```
   本当に呼び出し元ごとの資格情報が必要な場合は、argv に置く代わりにクライアント側でファイルから読み込みます。
   ```sh
   cornus tunnel --authtoken-file ~/.ssh/id_ed25519 web 80
   ```

- パスフレーズ付きの秘密鍵はサポートされません。暗号化されていない鍵を使うか、パスワード認証にフォールバックしてください。
- known-hosts ファイルもなく、固定した host key もなく、insecure の opt-in もない場合、未検証のホストを信頼せずに接続が拒否されます。
- SSH エージェントとの統合はありません。ここでの資格情報は生の鍵/パスワードとしてサーバーへ渡されるものであり、クライアントの `ssh-agent` がその都度生成する署名ではありません。エージェントベースの認証をサポートするには、サーバー側の SSH ハンドシェイクがクライアント側でしか到達できないエージェントに問い合わせる必要があります — つまりトンネル要求の接続を介したエージェントフォワーディングが必要ですが、このバックエンドはまだそれを実装していません。

**関連項目:** [cornus tunnel](/ja/cli/tunnel)、[パブリックトンネル](/ja/topics/tunnels)、[サーバー環境変数](/ja/reference/server-env-vars)

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

**関連項目:** [cornus tunnel](/ja/cli/tunnel)、[パブリックトンネル](/ja/topics/tunnels)

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

**関連項目:** [cornus tunnel](/ja/cli/tunnel)、[パブリックトンネル](/ja/topics/tunnels)、[Helm chart values](/ja/reference/helm-values)
