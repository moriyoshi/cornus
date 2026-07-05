# cornus storage

Cornus サーバーのストレージ消費量を、何も変更せずに報告します。

## 構文

```sh
cornus storage usage [flags]
```

## 説明

`cornus storage` はサーバーのストレージ管理をまとめたものです。現時点では、非破壊的なレポートを一つ提供します。領域の回収はサーバー側 (`POST /.cornus/v1/gc` エンドポイントと定期 GC スケジューラー) のままです。

`cornus storage usage` は `GET /.cornus/v1/storage` を取得し、現在の使用量を表示します: レジストリのコンテンツストア (ブロブ数と合計バイト数) と、ファイル単位のブロックキャッシュが有効な場合はその使用量。ガベージコレクションの読み取り専用の対応物であり、何も削除も退避もしません。

このレポートは、レジストリのすべてのブロブを列挙して stat することで算出されます。そのため、ファイルシステムバックエンドに対しては安価ですが、S3 のようなオブジェクトストアに対してはより高価です (ブロブごとに 1 回の `HEAD`)。タイトなループでポーリングするメトリクスではなく、たまに実行する運用者向けの問い合わせとして扱ってください。

このコマンドは、選択された接続プロファイル ([cornus config](/ja/cli/config) を参照) を通じてサーバーを解決するため、`--context`、トークン、TLS がすべて適用されます。1 回の実行だけエンドポイントを上書きするには `--server` を渡します。

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | リモート Cornus サーバーの URL (`http(s)://` または `ws(s)://`)。選択された接続プロファイルにフォールバックします。 |
| `--format` | — | `text` | 出力形式: `text` (人間が読みやすい形式) または `json` (生のレポート)。 |

JSON 形式には次のフィールドがあります (ブロックキャッシュが無効な場合、`fileCache*` は省略されます):

| フィールド | 説明 |
| --- | --- |
| `casBlobs` | レジストリのコンテンツストア内のブロブ数。純粋な再エクスポート構成 (コンテンツストアなし) ではゼロ。 |
| `casBytes` | それらのブロブの合計バイト数。 |
| `fileCacheBytes` | ファイル単位のブロックキャッシュのディスク上のサイズ。 |
| `fileCacheFiles` | ブロックキャッシュのファイル数。 |

## 例

人間が読みやすいレポートを表示します。

```sh
cornus storage usage
```

```
Registry CAS: 128 blobs, 3.4 GiB
Block cache:  12 files, 512.0 MiB
```

スクリプト向けに生のレポートを取得します。

```sh
cornus storage usage --format json
```

```json
{
  "casBlobs": 128,
  "casBytes": 3650722201,
  "fileCacheBytes": 536870912,
  "fileCacheFiles": 12
}
```

特定のサーバーに問い合わせます。

```sh
cornus storage usage --server https://cornus.example.com
```

## 関連項目

- [cornus config](/ja/cli/config) — このコマンドが解決に使用する接続プロファイル。
- ガベージコレクションはサーバー側です。サーバーリファレンスの `POST /.cornus/v1/gc` エンドポイントと `CORNUS_GC_INTERVAL` 定期スケジューラーを参照してください。
