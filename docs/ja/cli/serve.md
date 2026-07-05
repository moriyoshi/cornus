# cornus serve

OCI レジストリ、ビルドエンジン、デプロイエンジンからなる Cornus サーバーを 1 つのプロセスとして実行します。

## 構文

```sh
cornus serve [flags]
```

## 説明

`cornus serve` は `/v2/*` (OCI レジストリ) と `/.cornus/v1/*` (ビルド、デプロイ、exec、トンネルエンドポイント) をホストする統合 HTTP サーバーを開始します。中断されるまで (`Ctrl-C` または `SIGTERM`) 待ち受けます。

レジストリブロブとマニフェストは `--storage` で選ぶストレージバックエンドを通じて永続化されます。未設定ならストレージはデータディレクトリ配下です。対応する URL 形式は[ストレージバックエンド](/ja/reference/storage-backends)を参照してください。

## listen アドレスと公開範囲

`cornus serve` の既定の bind 先は **`:5000`、つまり全インターフェース**です。

これは意図的な既定値です。ワークロードはサーバーへ*逆方向に*接続します。コンテナ化された caretaker が、クライアントローカルマウント、クライアント側エグレス、資格情報の配布、ワークロードテレメトリーのためにサーバーへ接続を張るからです。ホストの `127.0.0.1` はコンテナのそれではないため、ループバックのみに bind するとこれらはいずれも接続できません。ワークロードがサーバーに到達する必要がある限り、既定値のままにしてください。

::: warning この API は既定では認証なしです
Cornus は[明示的に認証を設定しない限り認証なし](/ja/guides/security)であり、イメージのビルド、ワークロードのデプロイ、そのワークロードへの `exec` ができます。全インターフェースに bind した状態では、これらの権限はそのポートに経路を持つあらゆる相手が使えます。信頼できるネットワークの外にサーバーを公開する前に、[認証](/ja/guides/security)を有効にしてください。bind 範囲と認証の判断は一組です。
:::

サーバーをこのマシンだけに制限するには、明示的に指定します。

```sh
cornus serve --addr 127.0.0.1:5000  # このマシンのみ
cornus serve --addr 10.0.0.5:5000   # 特定のインターフェースのみ
```

`CORNUS_ADDR` でも同じことができます。bind 範囲を制限してよいのは、ワークロードがサーバーに到達する必要が**ない**場合です。つまり、クライアントローカルマウントも、クライアント側エグレスも、資格情報の配布も、ワークロードテレメトリーも使わない場合です。ループバックのみに bind した場合はサーバーが起動ログでその旨を伝えるので、接続側で原因不明のタイムアウトになる代わりに制限が可視化されます。

**コンテナ内**で動くサーバーは、全インターフェースに bind する必要があります。コンテナが自身のループバックに bind すると、公開ポート (`docker run -p` や Service) から到達できなくなるためです。公開コンテナイメージと Kubernetes マニフェスト / Helm チャートは `--addr :5000` を明示的に渡しています。イメージのコマンドを上書きする場合も、このフラグは残してください。コンテナ内のサーバーがそれでもループバックに bind した場合は、起動時に警告が出ます。

`--tls-cert` と `--tls-key` の両方を設定すると、サーバーは HTTPS を話します。`--tls-client-ca` を追加すると mutual TLS が有効になります。検証済みクライアント証明書の CommonName が呼び出し元 ID になり、クライアント証明書の提示自体は任意のままです。[セキュリティと認証](/ja/guides/security)を参照してください。

サーバーが受け入れる環境変数の全一覧は、[サーバー環境変数](/ja/reference/server-env-vars)を参照してください。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--addr` | `CORNUS_ADDR` | `:5000` | `/v2/*` と `/.cornus/v1/*` の HTTP listen アドレス。**既定では全インターフェース** — コンテナ化された caretaker がサーバーへ逆方向に接続するためです。このマシンだけに制限するには `--addr 127.0.0.1:5000` を指定します。[listen アドレスと公開範囲](#listen-アドレスと公開範囲) を参照。 |
| `--rootless` | `CORNUS_ROOTLESS` | `false` | ビルドエンジンをルートレスモード (ユーザー名前空間) で実行します。 |
| `--storage` | `CORNUS_STORAGE` | データディレクトリ | レジストリ永続化バックエンド: パス、`file://`、`mem://`、または `s3://bucket?region=&endpoint=&path_style=`。[ストレージバックエンド](/ja/reference/storage-backends)を参照。 |
| `--otel` | `CORNUS_OTEL` | `false` | 標準の `OTEL_*` 環境変数で OpenTelemetry (traces/metrics/logs) を有効にします。任意の `OTEL_*` exporter/endpoint 環境変数が設定されても暗黙に有効になります。 |
| `--tls-cert` | `CORNUS_TLS_CERT` | — | PEM 証明書ファイル。`--tls-key` とともに設定すると HTTPS を提供します。 |
| `--tls-key` | `CORNUS_TLS_KEY` | — | PEM 秘密鍵ファイル。`--tls-cert` とともに設定すると HTTPS を提供します。 |
| `--tls-client-ca` | `CORNUS_TLS_CLIENT_CA` | — | クライアント証明書 (mTLS) を検証する PEM CA bundle。検証済み証明書の CommonName が呼び出し元 ID になります。証明書提示は任意です。 |
| `--file-cache` | `CORNUS_FILE_CACHE` | `false` | 不変のクライアントローカルマウント読み取り向けに、サーバーのファイル単位キャッシュを有効にします。`--file-cache-dir` が必要です。 |
| `--file-cache-dir` | `CORNUS_FILE_CACHE_DIR` | — | ファイルキャッシュデータ用の必須ディレクトリ。専用ボリュームを使用してください。 |
| `--file-cache-chunk-size` | `CORNUS_FILE_CACHE_CHUNK_SIZE` | `1048576` | ファイルキャッシュブロックサイズ (bytes)。 |
| `--file-cache-max-bytes` | `CORNUS_FILE_CACHE_MAX_BYTES` | 無制限 | ガベージコレクションで適用するファイルキャッシュのソフトサイズ上限。 |

## 例

既定アドレス (全インターフェース) で提供し、データディレクトリ配下にデータを保存します。

```sh
cornus serve
```

特定のアドレスで listen し、レジストリをメモリ内に保持します。

```sh
cornus serve --addr :8080 --storage mem://
```

S3 互換ストレージへレジストリを永続化します。

```sh
cornus serve --storage 's3://my-bucket?region=us-east-1&path_style=true'
```

mutual TLS で HTTPS を提供します。

```sh
cornus serve \
  --tls-cert server.crt \
  --tls-key server.key \
  --tls-client-ca clients-ca.pem
```

## 関連項目

- [ストレージバックエンド](/ja/reference/storage-backends)
- [サーバー環境変数](/ja/reference/server-env-vars)
- [セキュリティと認証](/ja/guides/security)
- [アーキテクチャ](/ja/architecture/)
