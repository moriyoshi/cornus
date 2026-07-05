# cornus ingress-tunnel

単一のワークロードポートではなく、プロジェクト全体の**イングレス**を 1 つのパブリック URL で公開します。

## 構文

```sh
cornus ingress-tunnel [flags] <deployment>
cornus ingress-tunnel [flags] --project <project>
```

## 説明

[`cornus tunnel`](/ja/cli/tunnel)は 1 つのワークロードの 1 ポートを公開します。`cornus ingress-tunnel` は**イングレス**を公開します。[`x-cornus-ingress`](/ja/guides/ingress)でサービスが宣言したホスト名とパスのすべてが、ルーティングまで含めて 1 つの URL から到達可能になります。

この違いが効いてくるのは複数サービスのプロジェクトです。`cornus tunnel` では 3 つのサービスからなる Compose プロジェクトに 3 本のトンネルと互いに無関係な 3 つの URL が必要ですが、`cornus ingress-tunnel` なら 1 つで済み、しかも要求は本番環境とまったく同じようにホストとパスによって適切なサービスへ届きます。

トンネルが実際に前面に置くものはデプロイバックエンドによって異なります。

| バックエンド | 前面に置くもの |
| --- | --- |
| `kubernetes` (発見可能なイングレスコントローラーがある場合) | **実際のクラスターコントローラー**。クラスター自身のルーティング規則と TLS 証明書が適用されます |
| それ以外のすべてのバックエンド、およびコントローラーのないクラスター | **サーバー自身のイングレスルーティング**。宣言された同じホストとパスを提供します |

対象のデプロイメントがイングレスを宣言済みである必要があります。宣言していない場合、コマンドは何も応答しない URL を公開するのではなく、その旨を示して失敗します。

::: warning 開発およびプレビュー用途
**サーバー自身の**イングレスルーティングを前面に置くトンネルは、開発用の仕組みをパブリックなアドレスに置くことになります。自前の TLS 終端はなく、レート制限もアクセス制御も行わず、エラーページは到達できなかった内部ワークロード名とコンテナポートを表示します。進行中の作業の共有や Webhook の受信には適していますが、恒久的な用途には適しません。

**実際のクラスターイングレスコントローラー**を前面に置くトンネルには、これらの注意点は一切当てはまりません。クラスターがすでに強制しているルーティング、TLS、ポリシーがそのまま適用されます。どちらの状態にあるかは、コマンドが表示する `fronting:` の行でわかります。
:::

資格情報の扱いは[`cornus tunnel`](/ja/cli/tunnel)と同一です。secret はすでに認証済みのサーバーエンドポイントへ注入され、`--authtoken-file` と `CORNUS_TUNNEL_AUTHTOKEN` は secret を argv やシェル履歴に残さず、サーバーに既定資格情報がある場合は完全に省略できます。トンネルは `Ctrl-C` まで維持されます。

## Host の扱い

トンネルプロバイダーは `abc123.ngrok.app` のような自分自身のホスト名を発行しますが、イングレスは `web.myapp.example.com` のような別の名前で宣言されています。この 2 つをどう折り合わせるかを `--host-mode` で制御します。

| モード | アプリに届くもの | 使う場面 |
| --- | --- | --- |
| `auto` (既定) | プロバイダーにイングレスのホスト名を要求し、認められなければ `alias` にフォールバック | ほぼ常にこれ |
| `alias` | **トンネル**のホスト名。ルーティングはそれを介して解決される | アプリが受け取った `Host` から URL を組み立てる場合 |
| `passthrough` | 変更なし | トンネルのホスト名がすでにイングレスホストである場合、または raw TCP トンネルの場合 |
| `rewrite` | **イングレス**のホスト名 | アプリが設定済みのホスト名を前提としている場合 |

既定の結果は `alias` であり、これは意図的な選択です。アプリはブラウザーが実際に開いているホスト名を認識するため、リダイレクト、`Domain=` 付き Cookie、CORS オリジンのいずれも訪問者が到達できる先を指します。`rewrite` はこれを逆転させます。アプリは自身の設定済みホスト名を認識し、そこから生成される絶対 URL は訪問者が解決できない名前を指すことになります。`rewrite` は、認識しないホスト名を配信しないアプリの場合にのみ使ってください。

実際のクラスターイングレスコントローラーを前面に置いている場合、`rewrite` は利用できません。そのトンネルは raw バイトストリームであり、書き換えるべき HTTP レイヤーが存在しないためです。コマンドはフラグを黙って無視するのではなく、その旨を伝えます。

### 宣言したホスト名を実際に取得する

トンネルの公開ホスト名を任意に選べるバックエンドもあり、その場合 `auto` は `passthrough` に解決されます。要求に対する調整は一切行われず、TLS もエンドツーエンドにできます。`ngrok` はアカウントの予約ドメインまたはカスタムドメインで、`ssh` バックエンドは要求された bind ホストでルーティングするリレー ([sish](https://github.com/antoniomika/sish)など) でこれをサポートします。Cloudflare quick tunnel と Tailscale Funnel はサポートしていないため、そこでは `auto` は `alias` にフォールバックします。

## フラグ

| フラグ | 説明 |
| --- | --- |
| `--project <name>` | Compose プロジェクトのすべてのデプロイメントを 1 つの URL で公開します。deployment 引数とは排他です。 |
| `--host-mode <mode>` | `auto` (既定)、`passthrough`、`alias`、`rewrite`。上記を参照してください。 |
| `--host <hostname>` | スコープが複数のホストを提供する場合に、トンネルが前面に置く宣言済みイングレスホスト名。 |
| `--proto <http\|tcp>` | `http` (既定) または `tcp`。`tcp` トンネルは raw バイトストリームなので、クライアントの TLS と `Host` が変更されずにイングレスへ届きます — エンドツーエンド TLS を得る唯一の方法であり、その TLS を終端する**実際のクラスターイングレスコントローラーが必要**です。イングレスを自身でルーティングするサーバーは平文 HTTP を話すため、https で失敗する URL を公開せず `tcp` を拒否します。 |
| `--authtoken-file <path>` | トンネル資格情報をファイルから読み取り、argv とシェル履歴に残しません。 |
| `--authtoken <token>` | 資格情報を直接指定します。`ps` から見え、シェル履歴にも残りがちです。`--authtoken-file` を推奨します。 |
| `--forward-agent` | `ssh` バックエンド向けにローカルの `ssh-agent` をサーバーへ転送します。`ssh -A` と同様、信頼できるサーバーに対してのみ使用してください。 |
| `--server <url>` | リモート cornus サーバー URL。選択中の接続プロファイルへフォールバックします。 |

## 例

Compose プロジェクトを公開する — すべてのサービスを 1 つの URL で。

```sh
cornus ingress-tunnel --project myapp
```

```
Ingress tunnel for project/myapp ready at https://abc123.ngrok.app
  serving: web.myapp.example.com, api.myapp.example.com
  fronting: the cluster ingress controller
  host: passed through untouched
```

1 つのデプロイメントのイングレスを、資格情報をファイルから読み取って公開する。

```sh
cornus ingress-tunnel --authtoken-file ~/.config/cornus/ngrok-token web
```

アプリに設定済みホスト名を認識させる。

```sh
cornus ingress-tunnel --project myapp --host-mode rewrite --host web.myapp.example.com
```

## デプロイ時に自動で公開する

プロジェクトを起動するたびに公開したい場合は、コマンドを実行する代わりに Compose で宣言します。

```yaml
services:
  web:
    image: myapp:latest
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.myapp.example.com
      tunnel: true
```

これにより `cornus compose up` がプロジェクトのイングレスを公開して URL を表示し、セッション終了時にトンネルを削除します。資格情報は引き続きクライアント側から渡されます (プロファイルの `authtoken-file`、または `CORNUS_TUNNEL_AUTHTOKEN`)。リポジトリにコミットされる compose ファイルからは渡されません。

オブジェクト形式ではフラグと同じオプションを指定できます。

```yaml
    x-cornus-ingress:
      tunnel:
        host_mode: rewrite
        host: web.myapp.example.com
```

## 関連項目

- [`cornus tunnel`](/ja/cli/tunnel) — 単一のワークロードポートを公開する
- [イングレスガイド](/ja/guides/ingress) — ホストとパスを宣言する
- [トンネルガイド](/ja/guides/tunnels) — バックエンドごとの設定
