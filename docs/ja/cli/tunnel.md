# cornus tunnel

サーバーがホストするトンネルを通じてデプロイメントのポートをパブリックインターネットへ公開し、実行中アプリケーションをどこからでも到達可能にします。

## 構文

```sh
cornus tunnel [flags] <name> <port>
```

## 説明

`cornus tunnel` は、デプロイメントのポートへのパブリックトンネルをホストするよう Cornus サーバーに要求します。進行中の作業を共有したり、Webhook を受信したりする用途に便利です。サーバーはトンネルをプロセス内でホストしてワークロードへ接続するので、[`cornus port-forward`](/ja/cli/port-forward)と同様に、どのバックエンドでもワークロードが公開していないポートへ到達できます。ただしローカルリスナーではなくパブリック URL を提供します。

トンネルバックエンドは**サーバー**上で `CORNUS_TUNNEL_BACKEND` (既定は `ngrok`) により選択します。ほかに `ssh` (SSH reverse-tunneling)、`cloudflare` (Cloudflare トンネル)、`tailscale` (Tailscale Funnel) があります。必要な設定は[パブリックトンネル — バックエンド](/ja/topics/tunnels)を、バックエンドごとの段階的な設定手順は[トンネルガイド](/ja/guides/tunnels)を参照してください。

トンネルごとの資格情報は、すでに認証済みのサーバーエンドポイントへ注入されます (サーバーは事前には知り得ません)。資格情報の種類はバックエンド次第です。`ngrok` は ngrok authtoken、`ssh` は SSH 秘密鍵 (PEM) または password、`cloudflare` / `tailscale` は不要です (匿名または帯域外で参加済み)。`CORNUS_TUNNEL_AUTHTOKEN` 環境変数 (自動的に `--authtoken` へ読み込まれます。`NGROK_AUTHTOKEN` も legacy alias として引き続き使えます) または `--authtoken-file` で渡してください — どちらも secret を argv やシェル履歴に残しません。`--authtoken` 自体はそれらとは異なります。最善なのは、**サーバー**に既定資格情報がある場合に完全に省略することです (こちらも `CORNUS_TUNNEL_AUTHTOKEN` で設定しますが、サーバー自身の環境変数としてです — 同じ変数名でも、どちらのプロセスが読むかで意味が異なります)。コマンドはパブリック URL を表示し、`Ctrl-C` (または `SIGTERM`) までトンネルを維持します。終了するとトンネルは削除されます。

ポートは `1..65535` でなければなりません。接続は `--server`、または選択中の接続プロファイルから解決されます ([`cornus config`](/ja/cli/config)を参照)。全体像は[パブリックトンネル](/ja/topics/tunnels)を参照してください。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | リモート cornus サーバー URL (`http(s)://` または `ws(s)://`)。選択した接続プロファイルへフォールバックします。 |
| `--authtoken` | `CORNUS_TUNNEL_AUTHTOKEN` (`NGROK_AUTHTOKEN` も legacy alias として利用可) | — | トンネルバックエンドの資格情報 (例: ngrok authtoken)。サーバーへ注入します。サーバーに既定資格情報がある場合のみ省略できます。secret を argv やシェル履歴に残すため、このフラグ自体を直接使うのではなく、環境変数 (このフラグへ自動的に読み込まれ、argv には現れません) か `--authtoken-file` を優先してください。 |
| `--authtoken-file` | — | — | `--authtoken` の代わりに、このファイルから資格情報を読み込みます。secret を argv やシェル履歴に残しません。`--authtoken` とは併用できません。 |
| `--proto` | — | `http` | 公開プロトコル: `http` または `tcp`。 |

位置引数:

- `<name>` — 公開するデプロイメント名 (必須)。
- `<port>` — トンネルを通じて公開するコンテナポート (必須)。

## 例

`web` デプロイメントのコンテナポート 80 を HTTP で公開します。authtoken は環境変数から読み込むため、コマンドラインに `--authtoken` を指定する必要はありません。

```sh
export CORNUS_TUNNEL_AUTHTOKEN=2ab3...
cornus tunnel web 80
```

生の TCP ポートを公開します。資格情報は argv ではなくファイルから読み込みます。

```sh
cornus tunnel --proto tcp --authtoken-file ~/.config/cornus/ngrok-token db 5432
```

サーバーの既定資格情報に任せます (クライアント側では資格情報を一切指定しません)。

```sh
cornus tunnel web 8080
```
