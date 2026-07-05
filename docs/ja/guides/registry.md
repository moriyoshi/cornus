# レジストリとストレージ

Cornus には、コンテンツアドレス指定ストアを基盤とする小さな OCI レジストリ (`/v2/*`) が組み込まれています。このページでは、ストレージバックエンドの選択、イメージの出し入れ、領域の回収を扱います。バックエンドの一覧は、[ストレージバックエンド](/ja/reference/storage-backends)を参照してください。

## ファイルシステムストレージでレジストリを提供する

レジストリの CAS をローカルディスクに永続化します。`--storage` を指定しない場合の既定値です。

```sh
cornus serve --storage /var/lib/cornus     # または file:///var/lib/cornus
cornus serve                               # 未設定時はデータディレクトリに保存
```

- 裸のパスまたは `file://path` を指定すると、そのディレクトリの下に CAS レイアウトを書き込みます。再起動後もデータは残ります。
- `--storage` を省略すると、ストアはサーバーのデータディレクトリ (`--data-dir` / `CORNUS_DATA`) の下に置かれます。

**関連ページ:** [ストレージバックエンド](/ja/reference/storage-backends)、[cornus serve](/ja/cli/serve)

## 一時的なメモリ内ストレージで提供する

テストや使い捨てサーバー用に、レジストリ全体をプロセスのメモリ内に保持します。

```sh
cornus serve --storage mem://
```

- サーバーが停止するとすべて失われます。永続レジストリには使わないでください。

**関連ページ:** [ストレージバックエンド](/ja/reference/storage-backends)、[cornus serve](/ja/cli/serve)

## S3 または S3 互換ストレージで提供する

CAS を S3 バケットに保存し、ネイティブの S3 マルチパートアップロードとしてストリーミングします。

```sh
# AWS S3 (標準 AWS 資格情報チェーンを使用)
cornus serve --storage 's3://my-bucket?region=us-east-1'

# S3 互換ストレージ (MinIO など): エンドポイントとパス形式を指定
cornus serve --storage 's3://my-bucket?region=us-east-1&endpoint=http://localhost:9000&path_style=true&access_key=KEY&secret_key=SECRET'
```

- クエリーパラメーターは `region`、`endpoint`、`path_style`、明示的な `access_key` / `secret_key` です。省略時は標準 AWS 資格情報チェーンを使用します。
- 複数のレプリカが一つの `s3://` CAS を共有する場合、`CORNUS_GC_INTERVAL` を有効にするのは最大一つのレプリカだけにしてください。下のガベージコレクションも参照してください。

**関連ページ:** [ストレージバックエンド](/ja/reference/storage-backends)、[サーバー環境変数](/ja/reference/server-env-vars)

## GCS または Azure ブロブストレージで提供する

Google Cloud ストレージ (`gs://`) または Azure ブロブ (`azblob://`) を永続化バックエンドとして使います。

```sh
# これらのスキームには -tags cloudblob を付けたビルドが必要
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
cornus serve --storage 'gs://my-bucket'
cornus serve --storage 'azblob://my-container'
```

- 既定のバイナリでは、これらのスキームに対して「このビルドでは未対応」という明確なエラーを返します。有効にするには `-tags cloudblob` を付けてビルドしてください。
- 資格情報は標準の Google / Azure 資格情報チェーンから取得します。

**関連ページ:** [ストレージバックエンド](/ja/reference/storage-backends)、[cornus serve](/ja/cli/serve)

## レジストリへイメージをプッシュ、プルする

`cornus push` または標準の Docker ツールを使って、イメージをレジストリへ移動します。

```sh
# ローカルの OCI/docker-archive tarball をプッシュするか、レジストリ参照をコピー
cornus push ./app.tar localhost:5000/app:v1
cornus push docker.io/library/nginx:latest localhost:5000/nginx:latest

# 同じレジストリに対する標準 docker
docker push localhost:5000/app:v1
docker pull localhost:5000/app:v1
```

- `source` 引数がディスク上のファイルなら tarball として読み込みます。それ以外はレジストリ参照として扱います。
- `cornus push --insecure` (既定値は `true`) は平文 HTTP のレジストリを許可します。認証が有効な場合は `CORNUS_TOKEN` を設定すると、`cornus push` がレジストリ用のベアラー資格情報として送信します。

**関連ページ:** [cornus push](/ja/cli/push)、[イメージをビルドする](/ja/guides/building-images)

## 匿名プルを許可する

認証されていないクライアントによるプルを許可しつつ、プッシュと削除には引き続き認証を要求します。

```sh
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve   # 1/true/yes/on
```

- これは、ほかの認証が有効な場合にだけ意味を持ちます。`GET` / `HEAD` を `/v2/*` 下で開放しますが、ほかの操作には引き続き資格情報が必要です。
- 明示的な `pull` 規則を `CORNUS_API_POLICY` に設定した場合はこちらを上書きします。両方を設定すると、起動時に警告が出ます。

**関連ページ:** [セキュリティと認証](/ja/guides/security)

## クラスターランタイムへレジストリを通知する

デプロイ先に、ビルド済みイメージをどのレジストリホストからプルすべきか知らせます。

```sh
# host[:port]、または TLS レジストリには https://host
CORNUS_ADVERTISE_REGISTRY=cornus.example:5000 cornus serve
```

- サーバーは `GET /.cornus/v1/info` で、この値をデプロイ先がプルするレジストリとして公開します。未設定の場合は、サーバーへの到達方法から導出します。
- `CORNUS_ADVERTISE_URL` は別の設定です。Pod のマウントエージェントや caretaker が接続し直すクラスター内 Cornus URL を指定します。Kubernetes バックエンドでクライアントローカルマウントを使う場合に必要です。

**関連ページ:** [サーバー環境変数](/ja/reference/server-env-vars)、[リモートクラスター](/ja/guides/remote-clusters)

## 組み込みレジストリの代わりに外部 OCI レジストリを使う

すでに運用しているレジストリへプッシュし、そこからデプロイします。

```sh
# ビルド出力を任意の外部レジストリへコピー
CORNUS_TOKEN=$(cornus token issue --sub ci --hs256-secret "$SECRET") \
  cornus push ./app.tar registry.example.com/app:v1
```

- `cornus push` は任意の OCI レジストリ参照を対象にできます。Bearer トークンは宛先ホストだけを対象とするため、レジストリ間コピーで無関係なソースレジストリへトークンが漏れることはありません。
- リモートビルドでは、`CORNUS_REGISTRY` がレジストリ部分を省略したタグに使うレジストリホストを設定します。

**関連ページ:** [cornus push](/ja/cli/push)、[イメージをビルドする](/ja/guides/building-images)

## ガベージコレクションで領域を回収する

CAS に対してマークアンドスイープを実行し、参照されていないブロブと古いビルドキャッシュを削除します。

```sh
# 必要に応じて、実行中サーバーの GC エンドポイントへ POST
curl -X POST http://localhost:5000/.cornus/v1/gc

# 定期的に、同じ GC を間隔指定で実行 (Go の期間形式)
CORNUS_GC_INTERVAL=1h cornus serve
```

- `POST /.cornus/v1/gc` は破壊的なエンドポイントです。認証が有効な場合は、`gc` 操作を `CORNUS_API_POLICY` で制御します。
- `CORNUS_GC_INTERVAL` を設定しなければスケジューラーは無効です。不正な値または正でない値は起動時エラーになります。複数のレプリカが一つの `s3://` ストアを共有する場合は、最大一つのレプリカでだけ有効にしてください。`CORNUS_GC_LEASE` は Kubernetes Lease によるリーダー選出を追加します。

**関連ページ:** [サーバー環境変数](/ja/reference/server-env-vars)、[ストレージバックエンド](/ja/reference/storage-backends)
