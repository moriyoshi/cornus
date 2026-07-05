# ネットワークのレシピ

ワークロードへ接続するためのタスク指向レシピです。ポートごとの転送、SOCKS5 スプリットトンネル、ワークロード間 hub を扱います。ホスト型トンネルを介したワークロードの公開については、[トンネルガイド](/ja/guides/tunnels)を参照してください。これらのレシピの仕組みについては、[リモートワークフロー](/ja/topics/remote-workflows)と[ワークロード間 hub](/ja/topics/hub)を参照してください。

## ローカルポートをワークロードへ転送する

対応付けごとにローカルリスナーをバインドし、各接続をデプロイメントの最初のインスタンスへ転送します。公開されていないポートにも到達できます。

```sh
cornus port-forward web 8080:80 5432:5432
```

- 各対応付けは `LOCAL:REMOTE` (または素の `PORT`) で、任意で `/tcp` または `/udp` 接尾辞を付けられます。例: `cornus port-forward dns 5353:53/udp`。
- `--address 0.0.0.0` はすべてのインターフェースにバインドします。UDP は dockerhost/containerd/bare バックエンドで動作しますが、Kubernetes ポート転送は TCP 専用です。

**関連項目:** [cornus port-forward](/ja/cli/port-forward)、[リモートワークフロー](/ja/topics/remote-workflows)

## SOCKS5 スプリットトンネルプロキシを実行してサービス名で到達する

サービス接尾辞を持つホストをクラスターへトンネルし、それ以外へは直接接続するローカル SOCKS5 プロキシをバインドします。

```sh
cornus socks5
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

- `--service-host-suffix` (既定 `.cornus.internal`) で終わるホストは対応するサービスにトンネルされます。接尾辞を取り除いてサービス名を導出します。
- `--resolve 'PATTERN=REPLACE'` は高度な形式です (順序付きで最初の一致が採用され、sed 形式の `\1` 後方参照を使えます)。接尾辞の既定を置き換えます。

**関連項目:** [cornus socks5](/ja/cli/socks5)、[リモートワークフロー](/ja/topics/remote-workflows)

## デプロイまたは Compose セッションの conduit を選ぶ

`--server` セッションがワークロードポートを自分へ公開する方法として、ポートごとのリスナーまたは一つの SOCKS5 プロキシを選びます。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --conduit socks5
cornus compose up --conduit port-forward
```

- 優先順位は `--conduit`、`CORNUS_CONDUIT`、プロファイルモードです。`--no-forward-ports` は conduit 全体を無効にします。
- 値だけを指定するとモードだけを設定します。`socks5://host:port[?suffix=SUFFIX]` の URL はバインドアドレスとサービスホストの接尾辞も設定します。

**関連項目:** [リモートワークフロー](/ja/topics/remote-workflows)、[cornus deploy](/ja/cli/deploy)

## 一つのブラウザープロキシで Compose スタックと web UI 全体へ到達する

Compose スタックを SOCKS5 モードで起動し、`cornus web` UI も同じ共有 conduit に公開します。ブラウザーのプロキシ設定一つで、すべてのサービスと UI に名前で到達できます。

```sh
# 1. この接続の conduit を socks5 にする (プロファイルごとに一度)。
cornus config set-context --conduit-mode socks5

# 2. スタックを detached で起動する。socks5 モードではバックグラウンド
#    agent が共有プロキシを一つホストし、各サービスの短縮名を登録する。
cornus compose up -d

# 3. 同じ共有 conduit に web UI を公開する (ローカルポートは bind しない)。
cornus web --publish-in-conduit
```

ブラウザーの SOCKS5 プロキシを agent のプロキシ、既定では `127.0.0.1:1080`、に設定し、**リモート DNS** (SOCKS5h) を使います。一つの設定で `web.cornus.internal` の Compose サービス、ほかのサービスの短縮名、そして `cornus.internal` の web UI に到達できます。

- これらは同じバックグラウンド agent、接続、SOCKS5 プロキシを共有します。
- Compose の短縮名はセッションが **socks5** モードで動く場合だけ登録されます。既定の port-forward モードでは、サービスは完全なデプロイ名で解決します。
- conduit 設定はコマンド間で一致させます。同じ `--conduit` URL を指定するか、すべてプロファイルに任せます。

**関連項目:** [cornus web](/ja/cli/web)、[cornus compose](/ja/cli/compose)、[cornus socks5](/ja/cli/socks5)

## conduit 経由でワークロードのイングレスホストへ到達する

`x-cornus-ingress` で宣言されたホスト名 (例: `web.example.com`) に、実際の DNS なしで到達できます。SOCKS5 セッションで `--ingress-conduit` を指定します。

```sh
# native: 実際のクラスターイングレスコントローラーへトンネルする (Kubernetes と kube access が必要)。
cornus compose up --conduit socks5 --ingress-conduit native

# emulate: 生成した証明書を使うクライアント側リバースプロキシ (どの backend でも可)。
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate
curl --socks5-hostname 127.0.0.1:1080 \
  --cacert ~/.local/share/cornus/ingress-ca.pem https://web.example.com/
```

- **native** はブラウザーの SNI と `Host` を実際の controller に渡します。**emulate** は `Host`/path によってワークロードへプロキシし、ローカルで TLS を終端します。`mkcert -install` 済みなら mkcert の CA を使い、そうでなければ一度信頼する自己署名 CA を使います。
- 優先順位は `--ingress-conduit`、`CORNUS_INGRESS_CONDUIT`、プロファイルです。`off` は無効化します。`cornus setup` はクラスターを調べて既定を選びます。ブラウザーでは **リモート DNS** (SOCKS5h) を使います。

**関連項目:** [パブリックイングレス](/ja/topics/ingress)、[cornus config](/ja/cli/config)

## ワークロード間 hub に spoke として参加する

このホストをオーバーレイに接続して、ローカルサービスを提供し、またはオーバーレイサービスに名前で到達します。

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```

- `--register name=host:port` はローカルサービスを提供します (この spoke へ中継されるため、NAT 配下ホストも到達可能なままです)。`--reach name=listen_ip:port` はオーバーレイ内にローカルリスナーをバインドします。少なくとも一つが必要です。
- サーバーは `--server` または選択したプロファイルから解決されます。client-TLS material を持つプロファイルは現在 `hub` では拒否されます。

**関連項目:** [cornus hub](/ja/cli/hub)、[ワークロード間 hub](/ja/topics/hub)

## hub 経由でワークロード間のサービスをエクスポート・インポートする

Kubernetes にデプロイするワークロードでは、CLI ではなくデプロイスペックで hub membership を宣言します。

```yaml
name: api
image: localhost:5000/api:v1
hub:
  identity: api                 # policy identity (defaults to the deployment name)
  export:
    - { name: api, port: 8080 }
    - { name: udpecho, port: 9000, protocol: udp, deliver: true }
  import:
    - { name: db, ports: [5432] }
```

- `export` はこのワークロードがホストするサービス、`import` は到達するサービスを列挙します。インポートごとに synthetic loopback IP と DNS record が接続されるため、単純な `dial(peer)` が hub へ流れます。
- hub が直接到達できないエクスポートには `deliver: true` を設定します。`importDynamic` は動的 catalog discovery に opt-in します。`hub:` は kubernetes 専用です。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)、[ワークロード間 hub](/ja/topics/hub)
