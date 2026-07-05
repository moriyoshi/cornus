# CLI リファレンス

`cornus` は、小さな OCI レジストリ、プロセス内の BuildKit ベースビルドエンジン、命令的デプロイエンジンをまとめた単一バイナリです。同じバイナリがサーバーとして動作し、ワークロードのビルド、プッシュ、デプロイ、到達を行うクライアントにもなります。

```sh
cornus [global flags] <command> [command flags]
```

コマンドツリーは [kong](https://github.com/alecthomas/kong) で解析されます。組み込みの使用方法は `cornus --help` または `cornus <command> --help` で確認してください。

## グローバルフラグ

これらのフラグはルートコマンドにあり、すべてのサブコマンドに適用されます。

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--data-dir` | `CORNUS_DATA` | プラットフォームのデータディレクトリ | 永続データディレクトリ (レジストリ CAS + ビルドキャッシュ)。 |
| `--context` | `CORNUS_CONTEXT` | 現在のコンテキスト | cornus クライアント設定で使う接続プロファイル ([`cornus config`](/ja/cli/config)を参照)。設定の現在のコンテキストを上書きします。 |
| `--config-file` | `CORNUS_CONFIG` | プラットフォームのユーザー設定ディレクトリ | cornus クライアント設定ファイルへのパス。`$XDG_CONFIG_HOME` を尊重するプラットフォームのユーザー設定ディレクトリが既定です。 |
| `--output` | `CORNUS_OUTPUT` | `auto` | 出力レンダリング: `auto`、`plain`、`fancy`、`json`。[出力モード](/ja/guides/output-modes)を参照。 |
| `--context-file` | `CORNUS_CONTEXT_FILE` | 自動検出 | 明示的なプロジェクトコンテキスト上書きファイル (JSON、YAML、TOML の bare Context)。指定しない場合、Cornus は上方向に `cornus-context.{json,yaml,toml}` を検索します。 |
| `--no-context-file` | — | `false` | プロジェクトコンテキストの自動検出を無効にします。`--context-file` とは併用できません。 |
| `--trust-context-file` | `CORNUS_TRUST_CONTEXT_FILE` | `false` | 自動検出したプロジェクトコンテキストファイルの endpoint、資格情報、TLS フィールドを許可します。信頼できる作業ツリーでのみ使用してください。 |
| `--no-color` | — | `false` | 装飾付き出力の色を無効にします (レイアウトは維持)。`NO_COLOR` / `CLICOLOR=0` でも有効です。 |

`--output` の値は次のとおりです。

- `auto` - 端末では fancy、それ以外ではプレーン。
- `plain` - 決定的で色なし。
- `fancy` - 色とレイアウト。
- `json` - 機械可読な NDJSON。

完全な動作は[出力モード](/ja/guides/output-modes)を参照してください。

## コマンド

| コマンド | 説明 |
| --- | --- |
| [`cornus serve`](/ja/cli/serve) | cornus サーバー (レジストリ + ビルド + デプロイ) を実行します。 |
| [`cornus build`](/ja/cli/build) | コンテキストからイメージをビルドしてプッシュします。 |
| [`cornus push`](/ja/cli/push) | ローカルイメージをレジストリへプッシュします。 |
| [`cornus deploy`](/ja/cli/deploy) | デプロイスペックを適用します。 |
| [`cornus exec`](/ja/cli/exec) | cornus サーバーを通じ、デプロイメント内でコマンドを実行します (docker exec)。 |
| [`cornus port-forward`](/ja/cli/port-forward) | ローカル TCP ポートをデプロイメントのコンテナポートへ転送します。 |
| [`cornus socks5`](/ja/cli/socks5) | 名前でワークロードへ到達するローカル SOCKS5 スプリットトンネルプロキシを実行します。 |
| [`cornus tunnel`](/ja/cli/tunnel) | ホスト型トンネルによりデプロイメントのポートをパブリックインターネットへ公開します。 |
| [`cornus config`](/ja/cli/config) | リモート cornus サーバーへ到達するための接続プロファイル (コンテキスト) を管理します。 |
| [`cornus compose`](/ja/cli/compose) | Compose / devcontainer プロジェクト向けの Docker Compose 互換クライアントです。 |
| [`cornus web`](/ja/cli/web) | ループバック専用のブラウザー UI とクライアント側 BFF を提供します。 |
| [`cornus daemon`](/ja/cli/daemon) | Docker API フロントエンドと統合バックグラウンドエージェントの制御です。 |
| [`cornus hub`](/ja/cli/hub) | ワークロード間オーバーレイに spoke として参加します。 |
| [`cornus token`](/ja/cli/token) | bearer auth を持つサーバー向けの JWT を発行します。 |
| [`cornus version` / `cornus health`](/ja/cli/version-health) | cornus のバージョンを表示するか、実行中サーバーの health エンドポイントを確認します。 |

## 関連項目

- [Cornus とは](/ja/introduction/what-is-cornus)
- [インストール](/ja/introduction/installation)
- [クイックスタート](/ja/introduction/quick-start)
- [アーキテクチャ](/ja/architecture/)
