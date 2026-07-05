# cornus hub

どこからでも (たとえば開発者のラップトップから) Cornus のワークロード間オーバーレイへスポークとして参加します。

## 構文

```sh
cornus hub [flags]
```

## 説明

`cornus hub` は caretaker の hub 役割を再利用し、このホストをスポークとして Cornus オーバーレイに接続します。このホストが提供するサービスは配送用に登録されます。hub は入力トラフィックをこのスポークへ中継し、スポークがローカルターゲットへ接続するため、NAT 配下のホストでも hub から到達可能である必要はありません。このホストが到達するサービスでは、hub へ流し込むローカルループバックリスナーをバインドします。少なくとも一つの `--register` または `--reach` が必要です。

接続は `--server`、または選択された接続プロファイルから解決されます。プロファイルのトークン / kube-auth と自動ポート転送も含まれます ([`cornus config`](/ja/cli/config)を参照)。解決されたトークンは caretaker の WebSocket handshake に載り、プロファイルの TLS 材料は接続に使用されます。フラグは接続解決前に検証されるため、不正なフラグでポート転送が開始されることはありません。`Ctrl-C` (または `SIGTERM`) まで実行を続けます。

オーバーレイモデルは[ワークロード間 hub](/ja/topics/hub)を参照してください。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | cornus サーバーの Hub URL (`ws(s)://` または `http(s)://`)。選択した接続プロファイルへフォールバックします。 |
| `--identity` | — | — | この spoke の ID (hub ポリシーで使用)。 |
| `--register` | — | — | オーバーレイにローカルサービスを提供します。`name=host:port` (配送によりこの spoke へ中継)。繰り返し指定可。 |
| `--reach` | — | — | オーバーレイサービスへ到達します。`name=listen_ip:port` (ローカルリスナーをバインド)。繰り返し指定可。 |

## 例

ローカルで実行中のサービスをオーバーレイに提供します。

```sh
cornus hub --identity laptop --register api=127.0.0.1:8080
```

ローカル loopback ポートでオーバーレイサービスに到達します。

```sh
cornus hub --identity laptop --reach db=127.0.0.1:5432
```

両方を同時に行います。

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```
