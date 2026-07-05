# cornus socks5

Cornus サーバーのワークロードへ名前で到達する、クライアント側 SOCKS5 スプリットトンネルプロキシを実行します。

## 構文

```sh
cornus socks5 [flags]
```

## 説明

`cornus socks5` はローカル SOCKS5 プロキシをバインドします。`host:port` が解決ルールに一致する `CONNECT` の宛先、既定では `--service-host-suffix` を持つすべてのホスト (例: `web.cornus.internal`) は、サーバーのポート転送経路を通じてそのサービスにトンネルされます。それ以外の宛先にはこのマシンから直接接続します。`Ctrl-C` (または `SIGTERM`) までフォアグラウンドで動作します。

これは `cornus config set-context --conduit-mode socks5` で選ぶセッションごとの conduit モードのアドホック版です。プロファイルの SOCKS5 設定から開始し、明示的なフラグ上書きを適用します。SOCKS5 conduit とポート転送などの到達手段の関係は[ネットワークと conduit](/ja/guides/networking)を参照してください。

接続は `--server`、または選択中の接続プロファイルから解決されます ([`cornus config`](/ja/cli/config)を参照)。

### 解決ルール

`--service-host-suffix` は単純な方法です。接尾辞で終わるホストは対応するサービスにトンネルされ、接尾辞を取り除いてサービス名を導出します。`--resolve PATTERN=REPLACE` は高度な形式です。ルールは順序付きで最初に一致したものが採用され、接尾辞の既定を置き換えます。`PATTERN` は `host:port` に一致し、`REPLACE` は sed 形式の `\1` backreference を含む `service:port` を生成します。`PATTERN` は正規表現としてコンパイルできなければなりません。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | リモート cornus サーバー URL (`http(s)://` または `ws(s)://`)。選択した接続プロファイルへフォールバックします。 |
| `--listen` | — | `127.0.0.1:1080` | SOCKS5 プロキシをバインドするローカルアドレス (またはプロファイル値)。 |
| `--service-host-suffix` | — | `.cornus.internal` | `CONNECT` 対象を対応するサービスへトンネルするホスト接尾辞。他のホストは直接エグレスします。 |
| `--resolve` | — | — | 高度な解決ルール `PATTERN=REPLACE` (繰り返し可、順序付き、最初の一致が採用)。接尾辞の既定を置き換えます。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | プロファイル | kubeconfig で Pod へ直接接続する代わりに、トンネル接続を cornus サーバープロキシ経由にします (クラスタープロファイルのみ)。`--no-via-server` は直接経路を強制します。`CORNUS_VIA_SERVER` とプロファイルを上書きします。 |

## 例

既定のアドレスでプロキシを開始します。

```sh
cornus socks5
```

続いてクライアントをプロキシに向け、サービスへ名前で到達します。

```sh
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

カスタムアドレスにバインドし、別の service-host 接尾辞を使います。

```sh
cornus socks5 --listen 127.0.0.1:1085 --service-host-suffix .svc.local
```

接尾辞の既定ではなく高度な解決ルールを使います。

```sh
cornus socks5 --resolve '^(.+)\.internal:(\d+)$=\1:\2'
```
