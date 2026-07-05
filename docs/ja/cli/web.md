# cornus web

Cornus サーバーが管理するワークロードと Compose プロジェクト用のローカルブラウザー UI を提供します。

## 概要

```sh
cornus web [flags]
```

## 説明

`cornus web` は、組み込みの SolidJS アプリケーションとクライアント側のバックエンドフォーフロントエンド (BFF) を起動します。UI では、ワークロードのライフサイクルと詳細、Compose プロジェクトと `depends_on` グラフ、クライアントローカルマウント、トンネルと転送、設定ファイル、ストリーミングログ、対話型 exec ターミナルを確認できます。BFF はクライアント向けにワークロード統計ストリームも公開します。

Compose の構造、ローカルファイルのソース、稼働中のバックグラウンドエージェントセッションは、サーバーの平坦化されたワークロード API には含まれないため、BFF はクライアント上で動作します。他のクライアントコマンドと同じように、選択中の接続プロファイルを使用します。プロジェクトビューでは、このコマンドに渡した Compose ファイルを使用します。ファイルが見つからず明示指定もない場合、サーバーのワークロードビューは引き続き利用できますが、プロジェクトビューは空になります。

UI には認証がありません。既定のモードではループバックでのみ待ち受けます。`--addr` には `localhost` またはループバック IP リテラルを指定する必要があり、ワイルドカードアドレスや非ループバックアドレスは拒否されます。`--publish-in-conduit` を使うと、リスナーは一切バインドせず、ループバックにある SOCKS5 conduit (下記参照) を通じてのみ到達できます。そのため、どちらの方法でも認証なしの境界は変わりません。

## フラグ

| フラグ | 環境変数 | 既定 | 説明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | ループバック待ち受けアドレス。ポート `0` は空いているポートを選択します。`--publish-in-conduit` とは併用できません。 |
| `-H`, `--host` | `CORNUS_HOST` | プロファイル、次に `http://localhost:5000` | cornus サーバーのエンドポイント。 |
| `-f`, `--file` | — | Compose の自動検出 | Compose ファイル。繰り返し指定できます。 |
| `--env-file` | — | `.env` の自動検出 | Compose の変数展開に使う env ファイル。繰り返し指定でき、既定の自動検出を置き換えます。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose ディレクトリ名 | プロジェクト名。 |
| `--open` | — | `false` | 待ち受け開始後に既定のブラウザーで UI を開きます。 |
| `--frontend` | `CORNUS_WEB_FRONTEND` | 組み込みアセット | 分離したフロントエンド開発サーバーの URL。BFF 以外の要求をそこへリバースプロキシし、実際の BFF は同じオリジンに残します。 |
| `--mcp` / `--no-mcp` | — | `true` | agent クライアント向けの MCP (Model Context Protocol) サーバーを `/.cornus/mcp` に同居させます。`--no-mcp` で無効にします。 |
| `--mcp-stdio` | — | `false` | コマンドを起動する agent クライアント向けに、HTTP リスナーをバインドせず、stdin/stdout 上だけで MCP サーバーを提供します。ポートはバインドしません。`--publish-in-conduit` とは併用できません。 |
| `--publish-in-conduit` | — | `false` | ローカルポートをバインドする代わりに、バックグラウンドエージェント内で UI をホストして共有 SOCKS5 conduit に公開します。 |
| `--publish-name` | — | 接尾辞の apex (例: `cornus.internal`) | conduit 内で UI を公開するホスト名。`--publish-in-conduit` を暗黙的に指定します。 |
| `--publish-port` | — | `80` | 公開名が応答する conduit ポート。 |
| `--conduit` | `CORNUS_CONDUIT` | プロファイル | `--publish-in-conduit` で使う SOCKS5 conduit セレクター (bare の `socks5`、または `socks5://host:port[?suffix=SUFFIX]`)。 |

## UI とワークロードに共通する一つのブラウザープロキシ設定

SOCKS5 conduit を通じて Cornus サーバーのワークロードへ到達する場合、つまりブラウザーのプロキシを `cornus socks5` (または `cornus config set-context --conduit-mode socks5`) に設定し、`*.cornus.internal` 名を解決する場合、`cornus web` UI は別の `http://127.0.0.1:<port>` にあり、ブラウザー側に別の設定が必要です。`--publish-in-conduit` はこの分断をなくします。

```sh
cornus web --publish-in-conduit
```

この指定は UI のバックエンドをバックグラウンドエージェントへ渡します。エージェントはプロセス内リスナーでバックエンドを提供し、**共有** conduit の `cornus.internal` (service-host 接尾辞の apex) に公開します。UI はワークロードへ到達するのとまったく同じプロキシを通じ、`http://cornus.internal/` で応答します。両方に対してブラウザーのプロキシ設定は一つです。ローカルポートはバインドされないため、新たに公開されるものはありません。UI はプロキシが到達できる場所からだけ到達できます。

コマンドはフォアグラウンドで動作し続け、終了時 (または kill 時) に名前を取り下げます。エージェントが再起動すると、自動的に再公開します。

注意:

- ブラウザーはプロキシ経由で **remote** DNS (SOCKS5h) を使う必要があります。`cornus.internal` がローカルではなくプロキシで解決されるようにするためです。これは既存の `*.cornus.internal` ワークロード名と同じ要件です。
- 公開名では `http://` だけを提供します (`https://` ではありません)。
- ワークロードセッションも **socks5** conduit を使う必要があります。既定のポート転送モードで動作している場合でも、UI と完全なデプロイメント名によるワークロードの解決はできますが、Compose の短い名前 (例: `demo-web` としてデプロイされたサービスに対する `web.cornus.internal`) は解決できません。この alias を登録するのは socks5 モードのワークロードセッションだけです。
- ここで渡す conduit 設定はワークロードセッションが使う設定と一致させる必要があります。一致しない場合、二つのプロキシが一つのバインドアドレスで競合します。

## agent クライアント向け MCP エンドポイント

同じサーバーは `/.cornus/mcp` に [MCP](https://modelcontextprotocol.io) (Model Context Protocol) サーバーも同居させます。これにより Zed の Agent パネル、Claude Desktop などの agent クライアントは、UI と同じクライアント側機能を操作できます。ワークロードの一覧表示と操作、依存関係グラフとマウントの読み取り、ログの tail、単発コマンドの実行、許可リストにある Compose/env/設定ファイルの読み書きができます。既定で有効です。無効にするには `--no-mcp` を渡します。

MCP ツールは UI の BFF とまったく同じロジックを使う薄いアダプターなので、二つのインターフェースがずれることはありません。ストリーミングは UI 専用です。対話型ターミナルとライブのログ/統計ストリームは MCP の要求/応答モデルに合わないため、MCP では範囲を制限した `logs_tail` (直近 N 行) と単発の `exec_run` (取得した stdout/stderr/終了状態) を提供します。

agent はサーバーの[フライトレコード](/ja/cli/activity)も利用できます。これは「現在何が真か」ではなく、事後に「何が問題だったか」へ答えるものです。CLI と同じ `since`/`kind`/`unfinished` フィルターを持つ `activity_read` ツールと、`cornus://activity/unfinished` **リソース**、つまりサーバーと caretaker が開始したまま完了しなかった処理の集合を提供します。リソース形式が有用です。クライアントはファイルのように添付できるため、動作不良のデプロイメントについて尋ねられた agent は、直前のサーバーが処理途中で停止したことを最初から把握できます。どちらにもレコードとともに `liveInstance` が含まれます。これがないと、サービス中プロセス自身の未終了ライフタイムがクラッシュのように見えます。追跡 (`cornus activity --follow`) はログストリームと同じ理由で CLI 専用です。

MCP は UI の脅威モデルをそのまま継承します。同じループバック/認証なしの境界と、同じ DNS rebinding Host ガードを使います。`--publish-in-conduit` では MCP エンドポイントも UI と同じ SOCKS5 conduit に公開され、UI と同様に conduit の利用者へ `file_write` と `exec_run` が公開されます。そこで影響範囲を狭めたい場合は `--no-mcp` を使ってください。

多くの MCP クライアントは HTTP URL に接続するのではなく、stdio でコマンドを起動します。その場合は `cornus web --mcp-stdio` を実行してください。同じツール群を stdin/stdout 上で提供し、HTTP リスナーはバインドしません。ブラウザー UI と同じ接続プロファイルと Compose フラグを再利用します。診断は stderr へ送るため、stdout 上の JSON-RPC ストリームを壊しません。たとえばクライアントには次のように登録します。

```json
{
  "command": "cornus",
  "args": ["web", "--mcp-stdio", "-f", "compose.yaml"]
}
```

## ファイル編集と適用

エディターが扱えるのは、解決済みの Compose ファイル、env ファイル、クライアント設定ファイルだけです。任意のパスやパストラバーサル表現は拒否されます。プロジェクトを適用すると `cornus compose ... up -d` 相当が実行されるため、標準の Compose 収束処理とバックグラウンドエージェントの動作が正本です。

## 例

現在の接続プロファイルと自動検出された Compose ファイルを使い、空いているループバックポートで起動します。

```sh
cornus web --open
```

リモートサーバーとプロジェクトを明示的に指定します。

```sh
cornus web --host https://cornus.example.com:5000 \
  -f compose.yaml -p demo --addr 127.0.0.1:8080
```

実際の BFF を同じオリジンに保ちながら、Vite を別プロセスで起動してホットリロードを使います。

```sh
cornus web --frontend http://localhost:5173
```

UI とワークロードの両方へ一つのブラウザープロキシ設定で到達できるよう、UI を SOCKS5 conduit に公開します。

```sh
cornus config set-context --conduit-mode socks5   # ワークロードセッションも socks5 を使用
cornus socks5 &                                    # ブラウザーが接続するプロキシ
cornus web --publish-in-conduit                    # UI は http://cornus.internal/
```

[`cornus compose`](/ja/cli/compose)、[`cornus daemon`](/ja/cli/daemon)、[接続設定リファレンス](/ja/reference/connection-config)も参照してください。
