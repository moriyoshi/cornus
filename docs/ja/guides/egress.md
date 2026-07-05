# エグレス

リモートワークロードのアウトバウンドトラフィックは通常、ランタイムが存在する場所から出ていきます。クライアント側エグレスでは、代わりにそのトラフィックをワークロードをデプロイしたマシン経由で経路します。VPN、企業プロキシ、SASE ゲートウェイ、または呼び出し元側だけが認可されたエグレス経路を持つエアギャップドクラスター向けです。これは稼働中の `cornus deploy --server` セッションと Pod ごとの caretaker サイドカーを利用します。ワークロードに呼び出し元が発行したシークレットを渡す関連機能は [資格情報ブローキング](/ja/guides/credentials) です。*受信方向*、つまりワークロードにパブリックホスト名を与える方法は [イングレス](/ja/guides/ingress) を参照してください。

## 仕組み

### モード

クライアント側エグレスは、リモートコンテナのエグレスをクライアント側の接続地点経由で経路します。透明性が高まる順に 3 つのモードがあり、デプロイスペックの `egress:` ブロック、Compose のポータブル拡張 `x-cornus-egress:`、または `cornus deploy --egress` で明示的に有効化します。

| モード | 対応バックエンド | 仕組み |
| --- | --- | --- |
| `env` | すべて | 呼び出し元自身のプロキシ環境変数 (`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` / `ALL_PROXY`、OS から解決) をコンテナに伝搬します。プロキシはコンテナから到達可能である必要があります。中継はありません。 |
| `proxy` | kubernetes, dockerhost, containerd | caretaker がループバック上で HTTP CONNECT プロキシと SOCKS5 を実行し、アプリケーションのプロキシ環境変数がそこを指します。各接続はサーバー経由で終端点へトンネルされます。 |
| `transparent` | kubernetes, dockerhost, containerd | アプリケーションのすべての TCP を nftables リダイレクトで捕捉し、`SO_ORIGINAL_DST` で復元します。プロキシ環境変数を無視するアプリケーションも対象になります。 |

Compose では、プロジェクトレベルまたはサービスごとに `x-cornus-egress` 拡張を使います (サービスブロックはプロジェクト既定を完全に上書きします)。`x-` 接頭辞により、標準の Compose ツールでもファイルは有効なままです。標準のツールは `x-*` キーを無視します。

```yaml
x-cornus-egress:
  mode: proxy
  default: cluster
  rules:
    - pattern: "*.corp.example"
      route: client

services:
  worker:
    image: registry.example/worker:v1
    # This service inherits the project-level policy.
```

### ルーティング: 4 つの経路

各宛先はちょうど 1 つの経路に解決されます。

| 経路 | 意味 |
| --- | --- |
| `client` | クライアント側ネットワークへ中継します。稼働中の deploy-attach セッションが必要です。 |
| `gateway` | 永続的なエグレスノード (Cornus サーバー自身) へ中継します。`--detach` と一緒に動作します。 |
| `cluster` | Pod 自身のネットワークから直接エグレスします。ローカルで接続され、中継されません。 |
| `deny` | 接続を破棄します。 |

`default` は一致しない宛先に適用され、既定は `cluster` です。そのためエグレスを有効化してもクラスター内トラフィックが黙って逸らされることはありません。呼び出し元が宛先をクライアントまたは gateway へ振り分けることを選びます。中継モード (`proxy`、`transparent`) には稼働中のセッションが必要なので、`cornus deploy --detach` は `client` 経路を拒否します。永続的な切り離し済みエグレスでは、`gateway` / `cluster` / `deny` にだけ経路してください。サーバーが gateway になり、運用者が `CORNUS_EGRESS_GATEWAY=1` で明示的に有効化する必要があります。別の `gateway:` URL は意図的に対応されず、拒否されます。

ポリシーはホップごとに再評価されます。caretaker が分類し、サーバーが再チェックし (侵害された Pod が自分のルーティングを昇格できないようにするため)、クライアントが最後の防御として再チェックします。そのため 3 者が同じ判断に達します。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)、[cornus deploy](/ja/cli/deploy)

## リモートワークロードの外向きトラフィックを呼び出し元ネットワーク経由にする

VPN、企業プロキシ、外部ネットワークから隔離されたクラスターで利用するために、ワークロードのエグレスを自分のマシンのネットワーク経由でルーティングします。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --egress proxy
```

- `--egress` モードは `env` (呼び出し元のプロキシ環境変数を伝播。全バックエンドで利用でき、中継なし)、`proxy` (caretaker の転送プロキシをサーバー経由で中継)、`transparent` (nftables によるリダイレクト。プロキシ環境変数を無視するアプリケーションも対象) です。
- `client` 経路にはアクティブな deploy-attach セッションが必要です。そのため直接実行する `cornus deploy --detach` ではこの経路を指定できませんが、`cornus compose up -d` はバックグラウンドエージェントでセッションを保持します。Compose では `x-cornus-egress:` 拡張を使用します。

**関連項目:** [cornus deploy](/ja/cli/deploy)

## 特定の宛先だけを呼び出し元経由にする

順序付きの先勝ちルーティング規則を追加し、選択した宛先だけをクライアント経由にします。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route '*.corp.example=client' \
  --egress-route '10.0.0.0/8=cluster'
```

- 各規則は `PATTERN=ROUTE` です。経路は `client`、`gateway`、`cluster`、`deny` のいずれかで、`--egress-route` は繰り返し指定でき、最初の一致が採用されます。
- パターンは宛先ホスト (グロブ)、CIDR、明示的なポート (例: `api.example.com:443`) に一致します。

**関連項目:** [cornus deploy](/ja/cli/deploy)

## 既定のエグレス経路を設定する

どの規則にも一致しない宛先をどこへ送るかを選びます。既定は `cluster` なので、エグレスを有効にしてもクラスター内トラフィックが暗黙に迂回されることはありません。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-route 'api.internal=client' --egress-default deny
```

- `--egress-default` は `cluster` (既定)、`client`、`gateway`、`deny` のいずれかです。
- `client` 経路にはアクティブなセッションが必要です。永続的な切り離しエグレスでは、`gateway` / `cluster` / `deny` だけを経路として選択してください。

**関連項目:** [cornus deploy](/ja/cli/deploy)

## エグレスに PAC 形式のポリシースクリプトを使う

順序付きの規則リストの代わりに、ルーティングの判断を **PAC 互換の JavaScript プログラム** にできます。`FindProxyForURL(url, host)` 関数なので、既存の企業 PAC ファイルをそのまま差し込めます。エグレス仕様の `script:` (または CLI の `--egress-pac`) として設定し、存在する場合は `rules` に優先します。これはサンドボックス化された pure-Go JS エンジンにより評価されます。標準の pure PAC 組み込み関数 (`shExpMatch`、`dnsDomainIs`、`isInNet` など)、呼び出しごとに上限を設けた期限、エラーまたはタイムアウト時に **`deny` としてフェイルクローズド** となる処理を備えます。ランタイムに周囲の権限はありません。`require` は使えず、実際のネットワークや DNS I/O も行いません (名前は呼び出し元がすでに知っている宛先 IP にだけ解決)。`Date` / 乱数は決定的なので、caretaker、サーバー、クライアントの評価点で再現可能です。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-pac ./egress.pac
```

```js
function FindProxyForURL(url, host) {
  if (dnsDomainIs(host, ".corp.example")) return "PROXY client";
  if (shExpMatch(host, "*.blocked.example")) return "DENY";
  return "DIRECT";
}
```

`FindProxyForURL` の戻り値の文字列は次のように経路へ対応付けられます (最初の `;` 区切りのディレクティブを使います)。

| PAC の戻り値 | 経路 |
| --- | --- |
| `DIRECT` | `cluster` (Pod ネットワークから直接接続) |
| `DENY` / `BLOCK` | `deny` |
| `CLIENT` / `CLUSTER` / `GATEWAY` | その経路 (Cornus extension) |
| `PROXY client` / `PROXY gateway` / `PROXY cluster` | その経路 |
| `PROXY host:port` (具体的なプロキシ) | `client`。クライアントが実際のプロキシを保持し、適用します。 |
| 空、null、または認識されない値 | `default` |

同じプログラムは仕様の中にインラインで設定することもできます。

```yaml
egress:
  mode: proxy
  default: cluster
  script: |
    function FindProxyForURL(url, host) {
      if (dnsDomainIs(host, ".corp.example")) return "PROXY client";
      if (shExpMatch(host, "*.blocked.example")) return "DENY";
      return "DIRECT";
    }
```

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)、[cornus deploy](/ja/cli/deploy)
