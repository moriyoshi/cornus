# エグレス

リモートワークロードの外向きトラフィックを呼び出し元のネットワーク経由にする、タスク指向のレシピです。VPN、企業プロキシ、SASE ゲートウェイ、外部ネットワークから隔離されたクラスターで利用できます。仕組みについては、[クライアント側エグレス](/ja/topics/egress)と[デプロイスペック](/ja/reference/deploy-spec)を参照してください。代わりに呼び出し元が発行したシークレットをワークロードへ渡すには、[資格情報](/ja/guides/credentials)ガイドを参照してください。

## リモートワークロードの外向きトラフィックを呼び出し元ネットワーク経由にする

VPN、企業プロキシ、外部ネットワークから隔離されたクラスターで利用するために、ワークロードのエグレスを自分のマシンのネットワーク経由でルーティングします。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --egress proxy
```

- `--egress` モードは `env` (呼び出し元のプロキシ環境変数を伝播。全バックエンドで利用でき、中継なし)、`proxy` (caretaker の転送プロキシをサーバー経由で中継)、`transparent` (nftables によるリダイレクト。プロキシ環境変数を無視するアプリケーションも対象) です。
- `client` 経路にはアクティブな deploy-attach セッションが必要です。そのため直接実行する `cornus deploy --detach` ではこの経路を指定できませんが、`cornus compose up -d` はバックグラウンドエージェントでセッションを保持します。Compose では `x-cornus-egress:` 拡張を使用します。

**関連項目:** [クライアント側エグレス](/ja/topics/egress)、[cornus deploy](/ja/cli/deploy)

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

**関連項目:** [クライアント側エグレス](/ja/topics/egress)、[cornus deploy](/ja/cli/deploy)

## 既定のエグレス経路を設定する

どの規則にも一致しない宛先をどこへ送るかを選びます。既定は `cluster` なので、エグレスを有効にしてもクラスター内トラフィックが暗黙に迂回されることはありません。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-route 'api.internal=client' --egress-default deny
```

- `--egress-default` は `cluster` (既定)、`client`、`gateway`、`deny` のいずれかです。
- `client` 経路にはアクティブなセッションが必要です。永続的な切り離しエグレスでは、`gateway` / `cluster` / `deny` だけを経路として選択してください。

**関連項目:** [クライアント側エグレス](/ja/topics/egress)、[cornus deploy](/ja/cli/deploy)

## エグレスに PAC 形式のポリシースクリプトを使う

規則のリストを PAC 互換の `FindProxyForURL` プログラムで置き換えます。既存の企業 PAC ファイルをそのまま使えます。

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

- `--egress-pac` は `--egress-route` より優先します。戻り値は `DIRECT` なら `cluster`、`PROXY client` / `PROXY gateway` なら対応する経路、`DENY` なら破棄、一致しない場合は `--egress-default` に対応します。
- スクリプトはサンドボックスで実行されます (`require` なし、ライブ I/O なし)。エラーまたはタイムアウト時はフェイルクローズで `deny` になります。

**関連項目:** [クライアント側エグレス](/ja/topics/egress)、[cornus deploy](/ja/cli/deploy)
