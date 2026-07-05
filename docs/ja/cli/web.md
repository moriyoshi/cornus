# cornus web

Cornus サーバーが管理するワークロードと Compose プロジェクト用のローカルブラウザー UI を提供します。

## 概要

```sh
cornus web [flags]
```

## 説明

`cornus web` は、組み込みの SolidJS アプリケーションとクライアント側のバックエンドフォーフロントエンド (BFF) を起動します。UI では、ワークロードのライフサイクルと詳細、Compose プロジェクトと `depends_on` グラフ、クライアントローカルマウント、トンネルと転送、要求が通過する両側のイングレス設定、設定ファイル、ストリーミングログ、ファイルブラウザーと対話型 exec ターミナルを並べるワークスペース、そしてサーバー組み込みのオブザーバビリティストアを可視化するメトリクスダッシュボードを確認できます。agent クライアント向けの MCP サーバーも同居させます。

BFF はクライアント上で動作し、他のクライアントコマンドと同じように選択中の接続プロファイルを使用します。プロジェクトビューでは、このコマンドに渡した Compose ファイルを使用します。ファイルが見つからず明示指定もない場合、サーバーのワークロードビューは引き続き利用できますが、プロジェクトビューは空になります。

UI には認証がありません。既定のモードではループバックでのみ待ち受けます。`--addr` には `localhost` またはループバック IP リテラルを指定する必要があり、ワイルドカードアドレスや非ループバックアドレスは、[`--allow-non-loopback`](/ja/guides/web-ui#ホスト外へのバインド) を指定しない限り拒否されます。[`--publish-in-conduit`](/ja/guides/web-ui#ui-とワークロードに共通する一つのブラウザープロキシ設定) を使うと、リスナーは一切バインドせず、ループバックにある SOCKS5 conduit を通じてのみ到達できます。そのため、どちらの方法でも認証なしの境界は変わりません。

各画面の役割、ファイルエクスプローラーとワークスペースの挙動、メトリクスダッシュボード、MCP のツール群、そして以下のフラグを使う手順については、[ブラウザー UI](/ja/guides/web-ui) を参照してください。

## フラグ

| フラグ | 環境変数 | 既定 | 説明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | ループバック待ち受けアドレス。ポート `0` は空いているポートを選択します。`--publish-in-conduit` とは併用できません。 |
| `--allow-non-loopback` | — | `false` | `--addr` をワイルドカードアドレスまたは非ループバックアドレスへバインドできるようにします。[ホスト外へのバインド](/ja/guides/web-ui#ホスト外へのバインド)を参照してください。 |
| `--allow-host` | — | ループバックの名前 | ループバックの表記に加えて応答する Host ヘッダーの値。繰り返し指定できます。 |
| `-H`, `--host` | `CORNUS_HOST` | プロファイル、次に `http://localhost:5000` | cornus サーバーのエンドポイント。 |
| `-f`, `--file` | — | Compose の自動検出 | Compose ファイル。繰り返し指定できます。 |
| `--env-file` | — | `.env` の自動検出 | Compose の変数展開に使う env ファイル。繰り返し指定でき、既定の自動検出を置き換えます。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose ディレクトリ名 | プロジェクト名。 |
| `--open` | — | `false` | 待ち受け開始後に既定のブラウザーで UI を開きます。 |
| `--local-root` | — | プロジェクト + バインドマウント元 | ファイルエクスプローラーが閲覧できる追加のディレクトリ。`[LABEL=]DIR[:ro]` 形式で、繰り返し指定できます。[プロジェクトに書かれていないディレクトリを閲覧する](/ja/guides/web-ui#プロジェクトに書かれていないディレクトリを閲覧する)を参照してください。 |
| `--frontend` | `CORNUS_WEB_FRONTEND` | 組み込みアセット | 分離したフロントエンド開発サーバーの URL。BFF 以外の要求をそこへリバースプロキシし、実際の BFF は同じオリジンに残します。 |
| `--mcp` / `--no-mcp` | — | `true` | agent クライアント向けの MCP (Model Context Protocol) サーバーを `/.cornus/mcp` に同居させます。`--no-mcp` で無効にします。[agent クライアント向け MCP エンドポイント](/ja/guides/web-ui#agent-クライアント向け-mcp-エンドポイント)を参照してください。 |
| `--mcp-stdio` | — | `false` | コマンドを起動する agent クライアント向けに、HTTP リスナーをバインドせず、stdin/stdout 上だけで MCP サーバーを提供します。ポートはバインドしません。`--publish-in-conduit` とは併用できません。 |
| `--publish-in-conduit` | — | `false` | ローカルポートをバインドする代わりに、バックグラウンドエージェント内で UI をホストして共有 SOCKS5 conduit に公開します。[UI とワークロードに共通する一つのブラウザープロキシ設定](/ja/guides/web-ui#ui-とワークロードに共通する一つのブラウザープロキシ設定)を参照してください。 |
| `--publish-name` | — | 参加した conduit の接尾辞 apex (例: `cornus.internal`) | conduit 内で UI を公開するホスト名。`--publish-in-conduit` を暗黙的に指定します。 |
| `--publish-port` | — | `80` | 公開名が応答する conduit ポート。 |
| `--conduit` | `CORNUS_CONDUIT` | 既存の conduit へ参加 | `--publish-in-conduit` で使う SOCKS5 conduit セレクター (bare の `socks5`、または `socks5://host:port[?suffix=SUFFIX]`)。アドレスや接尾辞を指定すると、その設定が**固定**されます。 |

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

プロジェクトに書かれていないディレクトリをファイルエクスプローラーへ渡します。

```sh
cornus web --local-root ~/scratch --local-root notes=~/wiki:ro
```

コマンドを起動する agent クライアント向けに、MCP エンドポイントだけを stdio で提供します。

```sh
cornus web --mcp-stdio -f compose.yaml
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

[ブラウザー UI](/ja/guides/web-ui)、[`cornus compose`](/ja/cli/compose)、[`cornus daemon`](/ja/cli/daemon)、[接続設定リファレンス](/ja/reference/connection-config)も参照してください。
