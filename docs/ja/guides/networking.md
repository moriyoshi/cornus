# ネットワークと conduit

ワークロードへ接続するためのタスク指向レシピです。ポートごとの転送、SOCKS5 スプリットトンネル、そしてその 2 つを選ぶセッションの conduit を扱います。ホスト型トンネルを介したワークロードの公開については[トンネルガイド](/ja/guides/tunnels)を、ワークロード*どうし*の接続については[ワークロード間 hub](/ja/guides/hub)を参照してください。

## セッションの conduit: ポート転送と SOCKS5

セッションがワークロードを呼び出し元に公開する方法が **conduit モード** です。既定はポートごとの転送です (公開済みポートごとにローカルリスナー 1 つ。Compose 互換)。明示的に選ぶ代替は、単一のクライアント側 **SOCKS5 スプリットトンネルプロキシ** です。サービスホスト接尾辞 (既定 `.cornus.internal`) 配下のホスト名は、一致するワークロードへ名前でトンネルされ、それ以外の宛先は自分のマシンから直接接続されます。1 つのプロキシですべてのサービスに名前で到達でき、ポートごとのリスナーは不要です。

```sh
# Make SOCKS5 the conduit for a profile, so compose up / deploy --server use it:
cornus config set-context demo --conduit-mode socks5
# Pin the shared proxy's bind address and suffix in one value:
cornus config set-context demo --conduit-mode 'socks5://.shared:1085?suffix=.demo.internal'

# Per-run override (flag > CORNUS_CONDUIT > profile > default port-forward):
cornus compose up --conduit socks5                    # join the shared proxy
cornus compose up --conduit 'socks5://'               # own proxy, ephemeral port
cornus deploy --server http://cornus.example:5000 --conduit socks5 -f deploy.yaml
```

値だけを指定した場合 (または `socks5://.shared`) は、プロファイルの共有プロキシに参加します。authority 部分を持つ `socks5://` URL は、それと共存するプライベートなセッション専用プロキシを起動します。SOCKS5 モードでは、サーバーごとの共有プロキシが `cornus daemon docker` のコンテナも対象にするため、1 つのプロキシで Docker コンテナと Compose サービスの両方へ名前で到達できます。SOCKS5 CONNECT は TCP 専用です。単独で使うアドホックなプロキシは [`cornus socks5`](/ja/cli/socks5) です。

**関連項目:** [接続設定](/ja/reference/connection-config)、[リモートクラスターで作業する](/ja/guides/remote-clusters)

## ローカルポートをワークロードへ転送する

対応付けごとにローカルリスナーをバインドし、各接続をデプロイメントの最初のインスタンスへ転送します。公開されていないポートにも到達できます。

```sh
cornus port-forward web 8080:80 5432:5432
```

- 各対応付けは `LOCAL:REMOTE` (または素の `PORT`) で、任意で `/tcp` または `/udp` 接尾辞を付けられます。例: `cornus port-forward dns 5353:53/udp`。
- `--address 0.0.0.0` はすべてのインターフェースにバインドします。UDP は dockerhost/containerd/bare バックエンドで動作しますが、Kubernetes ポート転送は TCP 専用です。

**関連項目:** [cornus port-forward](/ja/cli/port-forward)

## SOCKS5 スプリットトンネルプロキシを実行してサービス名で到達する

サービス接尾辞を持つホストをクラスターへトンネルし、それ以外へは直接接続するローカル SOCKS5 プロキシをバインドします。

```sh
cornus socks5
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

- `--service-host-suffix` (既定 `.cornus.internal`) で終わるホストは対応するサービスにトンネルされます。接尾辞を取り除いてサービス名を導出します。
- `--resolve 'PATTERN=REPLACE'` は高度な形式です (順序付きで最初の一致が採用され、sed 形式の `\1` 後方参照を使えます)。接尾辞の既定を置き換えます。

**関連項目:** [cornus socks5](/ja/cli/socks5)

## デプロイまたは Compose セッションの conduit を選ぶ

`--server` セッションがワークロードポートを自分へ公開する方法として、ポートごとのリスナーまたは一つの SOCKS5 プロキシを選びます。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --conduit socks5
cornus compose up --conduit port-forward
```

- 優先順位は `--conduit`、`CORNUS_CONDUIT`、プロファイルモードです。`--no-forward-ports` は conduit 全体を無効にします。
- 値だけを指定するとモードだけを設定します。`socks5://host:port[?suffix=SUFFIX]` の URL はバインドアドレスとサービスホストの接尾辞も設定します。

**関連項目:** [cornus deploy](/ja/cli/deploy)

## 一つのブラウザープロキシで Compose スタック全体と Web UI へ到達する

Compose スタックを SOCKS5 モードで起動し、`cornus web` UI を同じ共有 conduit に公開すると、一つのブラウザープロキシ設定ですべてのサービスと UI へ名前で到達できます。

```sh
# 1. この接続の conduit を socks5 にする (プロファイルごとに一度)。
cornus config set-context --conduit-mode socks5

# 2. スタックを detached で起動する。socks5 モードではバックグラウンド
#    agent が共有プロキシを一つホストし、各サービスの短縮名を登録する。
cornus compose up -d

# 3. 同じ共有 conduit に web UI を公開する (ローカルポートは bind しない)。
cornus web --publish-in-conduit
```

ブラウザーの SOCKS5 プロキシを agent のプロキシ (`cornus socks5` またはプロファイルの待ち受けアドレス。既定は `127.0.0.1:1080`) に向け、**リモート DNS** (SOCKS5h) を使います。一つの設定で次のすべてへ到達できます。

- `http://web.cornus.internal/` — `web` という Compose サービス (socks5 モードの `compose up` が短縮名を登録)。
- `http://db.cornus.internal:5432/` — 同じく短縮名で到達するほかのサービス。
- `http://cornus.internal/` — `cornus web` UI。

仕組みは次のとおりです。

- 3 つすべてが一つのバックグラウンド agent、一つの接続、一つの SOCKS5 プロキシを共有します。`compose up -d`、`cornus daemon docker`、`cornus web --publish-in-conduit` は、接続とその socks5 設定をキーにした同じ共有 conduit へ参加します。
- Compose の*短縮*名 (`demo-web` というデプロイ名ではなく `web`) を解決できるのは、ワークロードセッションが **socks5** モードで動く場合だけです。既定の port-forward モードでも UI と完全なデプロイ名 (`demo-web.cornus.internal`) は解決できますが、短縮名は解決できません。
- Web UI 自身はポートをバインドしません。プロキシが到達できる場所だけから到達可能なため、新たな公開面を追加せず、プロキシのループバック境界を継承します。
- conduit 設定はコマンド間で一致させてください。同じ `--conduit` URL を使うか、すべてプロファイルに任せます。`listen` / `suffix` の値が異なると、後から起動したコマンドのプロキシが最初のプロキシとバインドアドレスを奪い合います。

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

- **native** はブラウザーの SNI / `Host` を実際のコントローラーへそのまま渡し、クラスター証明書を使ってルーティングと TLS 終端を行います。**emulate** は `Host` / パスに基づいてワークロードへプロキシし、ローカルで TLS を終端します。インストール済みなら [mkcert](https://github.com/FiloSottile/mkcert) の CA (`mkcert -install` 後はブラウザーが自動的に信頼) で署名し、なければ一度だけ信頼する自己署名 CA (`~/.local/share/cornus/ingress-ca.pem`) を使います。
- 優先順位は `--ingress-conduit` > `CORNUS_INGRESS_CONDUIT` > プロファイル (`cornus config set-context --ingress-conduit`) です。`off` で無効化します。`cornus setup` はクラスターを調べて既定を選びます。ブラウザーでは **リモート DNS** (socks5h) を使います。

**関連項目:** [イングレス](/ja/guides/ingress)、[cornus config](/ja/cli/config)
