# レジストリストレージバックエンド

cornus レジストリは sha256 **コンテンツアドレス指定ストア** (CAS) 上に構築された小さな OCI レジストリ (`/v2/*`) です。イメージレイヤー、設定、マニフェストなど各ブロブは自身の byte の digest をキーにするため、同一 content は一度だけ保存され、再 hash で integrity を検証できます。マニフェストとタグはストアへの薄い参照です。

CAS が**存在する場所**は差し替え可能で、[`cornus serve`](/ja/cli/serve) の `--storage` フラグ (環境変数 `CORNUS_STORAGE`) で選びます。既定はサーバーデータディレクトリ下のファイルシステム layout です。

upload は**すべてのバックエンドで resumable**です (S3 バックエンドはネイティブ multipart upload としてストリーム)。領域は必要に応じて `POST /.cornus/v1/gc` で回収されます。これは CAS の mark-and-sweep を実行し stale ビルドキャッシュを prune します。`CORNUS_GC_INTERVAL` により periodic にも実行できます。[サーバー環境変数](/ja/reference/server-env-vars)を参照してください。

## バックエンド

| `--storage` 値 | バックエンド | 永続性 | 備考 |
| --- | --- | --- | --- |
| パスまたは `file://path` | ファイルシステム (既定) | ローカル disk 上で永続的 | `--storage` 未設定時の既定。データディレクトリ下のファイルシステム layout。 |
| `mem://` | メモリ内 | 一時的 | 再起動で失われます。test や throwaway サーバー向け。 |
| `s3://bucket?…` | AWS S3 / S3-compatible | object ストレージ上で永続的 | ネイティブ multipart upload。query param で region、エンドポイント、パス style を調整。 |
| `gs://bucket` | Google Cloud ストレージ | object ストレージ上で永続的 | `-tags cloudblob` ビルドが必要。 |
| `azblob://container` | Azure ブロブストレージ | object ストレージ上で永続的 | `-tags cloudblob` ビルドが必要。 |

```sh
cornus serve --storage /var/lib/cornus                       # filesystem (default)
cornus serve --storage mem://                                # in-memory (ephemeral)
cornus serve --storage 's3://my-bucket?region=us-east-1'     # AWS S3
```

## ファイルシステム

既定です。素のパス (または `file://path`) を渡すとレジストリはその下に CAS layout を書きます。`--storage` を完全に省略するとストアはサーバーデータディレクトリ下です。

## メモリ内

`mem://` は CAS 全体をプロセス memory に保持します。一時的なのでサーバー停止時にすべて失われます。test と短命サーバーに適し、永続レジストリには向きません。

> ホストバックエンドでは、`/v2/*` は [ローカルの Docker/containerd ストアの再エクスポートを既定とし](/ja/reference/server-env-vars#reusing-a-local-image-store) (`CORNUS_REGISTRY_SOURCE=host-native`) 、コンテンツストアを **一切保持しません** — インメモリのものすら持ちません。再エクスポートの下に CAS を重ねる (ユニオンビュー) には `--storage` を渡し、従来の永続レジストリにするには `CORNUS_REGISTRY_SOURCE=off` を設定します。

## S3 と S3-compatible

`s3://bucket` は CAS を S3 bucket に保存し、upload をネイティブ S3 multipart upload としてストリームします。query パラメーターで接続を設定します。

| Param | 意味 |
| --- | --- |
| `region` | bucket の AWS region。 |
| `endpoint` | S3 エンドポイントの上書き (MinIO など S3-compatible サービス用)。 |
| `path_style` | path-style addressing を使うには `true` (多くの S3-compatible サービスで必要)。 |
| `access_key` / `secret_key` | 明示資格情報 (それ以外は標準 AWS 資格情報 chain)。 |

```sh
# S3-compatible (MinIO, and similar): override endpoint + path-style
cornus serve --storage 's3://my-bucket?region=us-east-1&endpoint=http://localhost:9000&path_style=true&access_key=KEY&secret_key=SECRET'
```

複数レプリカが一つの `s3://` CAS を共有すると、各レプリカの interval GC は相互調整なしに動作します。協調 GC が実装されるまでは、`CORNUS_GC_INTERVAL` は最大一つのレプリカだけで有効にするか、on-demand の `POST /.cornus/v1/gc` を使ってください。

## Google Cloud ストレージと Azure ブロブ (`-tags cloudblob`)

`gs://` (GCS) と `azblob://` (Azure ブロブ) は gocloud ブロブ abstraction 経由で動作しますが、driver は Google/Azure SDK を取り込みます。既定 binary を lean に保つため**ビルドタグの背後**にあります。`-tags cloudblob` でビルドすると有効です。既定ビルドはこれらの scheme に明確な「このビルドではサポートされない」エラーを返します。

```sh
# Enable the Google Cloud Storage / Azure Blob backends:
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
cornus serve --storage 'gs://my-bucket'
```

`s3://` / `gs://` / `azblob://` の資格情報は標準 cloud 資格情報 chain から取得されます。

## データディレクトリと永続化

どの CAS バックエンドを選んでも、サーバーは working 状態 (ファイルシステム CAS、`--storage` がパスまたは未設定の場合)、in-progress upload、ビルドキャッシュを `--data-dir` (環境変数 `CORNUS_DATA`) で指定した **データディレクトリ** に保持します。

```sh
cornus serve --data-dir /var/lib/cornus       # or CORNUS_DATA=/var/lib/cornus
```

- コンテナの再起動後も残すにはデータディレクトリを永続ボリュームで支えます。名前付きボリューム (Docker) または PVC (提供される StatefulSet は `volumeClaimTemplates` を使い `/var/lib/cornus` をマウント) です。
- `--storage` がオブジェクトストア (`s3://`、`gs://`、`azblob://`) を指す場合、CAS はデータディレクトリではなく bucket に置かれます。永続的ブロブストレージはローカルボリュームに依存しなくなりますが、データディレクトリには引き続きビルドキャッシュと upload が保存されます。

## 関連項目

- [`cornus serve`](/ja/cli/serve) — `--storage` フラグとサーバーのその他の機能。
- [サーバー環境変数](/ja/reference/server-env-vars) — `CORNUS_STORAGE`、`CORNUS_GC_INTERVAL`、関連設定。
