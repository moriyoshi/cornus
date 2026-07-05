# パブリックトンネル

**パブリックトンネル** (`cornus tunnel`) は、ホスト型の中継経由でワークロードポートをパブリックインターネットに公開します。クラスターネイティブな Ingress リソースは不要で、経路上に公開ポートも必要ありません。進行中の作業を共有したり、Webhook を受けたり、スマートフォンでテストしたりする用途に使えます。ホスト型の中継ではなく実際の Ingress リソースによる永続的なホスト名が必要な場合は、[イングレス](/ja/topics/ingress)を参照してください。

`cornus tunnel <name> <port>` はワークロードポート用の **パブリック HTTPS URL** を返し、`Ctrl-C` まで起動し続けます。Cornus **サーバー** がトンネルをホストし、ポート転送と同じバイトブリッジ経由で各受信接続をワークロードへ中継します。そのため、Docker ホスト、containerd、Kubernetes の任意のバックエンドで、ワークロードが公開していないポートにも到達できます。

```sh
cornus tunnel --server http://cornus.example:5000 \
  --authtoken "$NGROK_AUTHTOKEN" web 80
```

トンネル資格情報は、すでに認証済みの要求にクライアントが注入します。サーバーは事前にそれを知りません。運用者は代わりにサーバー側の既定資格情報として `CORNUS_TUNNEL_AUTHTOKEN` を設定できます。その場合、呼び出し元は `--authtoken` を省略できます。HTTP または生の TCP は `--proto http` / `--proto tcp` で選びます。完全なフラグセットは [`cornus tunnel`](/ja/cli/tunnel) を参照してください。

## バックエンド

トンネルバックエンドはサーバー側で `CORNUS_TUNNEL_BACKEND` により選択します (既定は `ngrok`)。具体的なバックエンドは差し替え可能で、選択されたものだけが有効になります。

| バックエンド | 注入する資格情報 | 備考 |
| --- | --- | --- |
| `ngrok` (既定) | ngrok の authtoken (`NGROK_AUTHTOKEN`) | プロセス内の ngrok エージェント。子プロセスなし |
| `ssh` | SSH 秘密鍵 (PEM) またはパスワード | 自前でホスト可能なトンネルサーバー (sish、serveo、pinggy、localhost.run、GatewayPorts 付きプレーン `sshd`) への SSH リモート転送。バイナリ内の SSH スタックを再利用 |
| `cloudflare` | なし (匿名) | `cloudflared` バイナリ (`CORNUS_TUNNEL_CLOUDFLARED_BIN`) 経由の Cloudflare クイックトンネル |
| `tailscale` | なし | `tailscale` バイナリ経由の Tailscale Funnel。ノードは帯域外で tailnet に参加するため、ノードごとに Funnel は 1 つ |

`ssh` バックエンドでは、エンドポイントを `CORNUS_TUNNEL_SSH_ADDR` / `CORNUS_TUNNEL_SSH_USER` で設定し、host-key verification を `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` または `CORNUS_TUNNEL_SSH_HOSTKEY` で設定します (fail-closed。開発のみでは `CORNUS_TUNNEL_SSH_INSECURE=1`)。

バックエンドごとの手順は [トンネルガイド](/ja/guides/tunnels) を参照してください。環境変数の完全な一覧は [サーバー環境変数](/ja/reference/server-env-vars) にあります。
