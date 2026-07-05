# イメージをビルドする

プロセス内 BuildKit エンジン向けのタスク指向のレシピです。ローカルでもリモート Cornus サーバー上でも実行できます。すべてのフラグとその挙動は [cornus build](/ja/cli/build) を参照してください。

## Dockerfile をビルドして組み込みレジストリへプッシュする

コンテキストディレクトリから `-t` で指定した名前のイメージをビルドし、対象レジストリへプッシュします。

```sh
cornus build -t localhost:5000/app:latest .
```

- 位置指定コンテキストの既定は `.` です。既定以外の Dockerfile パスを使う場合は `-f docker/Dockerfile` を使います (コンテキストからの相対パス)。
- `--insecure` (既定 `true`) は `localhost:5000` のような plain-HTTP レジストリへのプッシュを許可します。

**関連ページ:** [cornus build](/ja/cli/build), [レジストリ](/ja/guides/registry)

## プッシュせずにビルドする (--no-push)

イメージだけをビルドし、レジストリには何も残しません。

```sh
cornus build -t localhost:5000/app:latest --no-push .
```

- タグを公開せずに Dockerfile を検証したりキャッシュを温めたりするのに便利です。

**関連ページ:** [cornus build](/ja/cli/build)

## ビルド引数を渡す (--build-arg)

Dockerfile 内の `ARG` が使用するビルド時の変数を設定します。

```sh
cornus build -t localhost:5000/app:latest \
  --build-arg VERSION=1.2.3 \
  --build-arg COMMIT=$(git rev-parse --short HEAD) .
```

- `--build-arg` は繰り返し指定可能で、フラグ 1 つにつき `KEY=VALUE` を 1 つ指定します。

**関連ページ:** [cornus build](/ja/cli/build)

## ビルドキャッシュマウントを使う (RUN --mount=type=cache)

パッケージやコンパイラのキャッシュディレクトリをビルド間で永続化します。これは Dockerfile の機能であり、CLI フラグは不要です。

```dockerfile
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl
```

```sh
cornus build -t localhost:5000/app:latest .
```

- キャッシュはビルドエンジン内に存在します。同じホストまたはリモートビルダー上ではビルド間で保持されます。

**関連ページ:** [cornus build](/ja/cli/build)

## シークレットをビルドに渡す (--secret id=NAME,src=PATH)

シークレットファイルをイメージに焼き込まず、`RUN --mount=type=secret` のステップにマウントします。

```sh
cornus build -t localhost:5000/app:latest \
  --secret id=npmrc,src=$HOME/.npmrc .
```

```dockerfile
RUN --mount=type=secret,id=npmrc,target=/root/.npmrc npm ci
```

- `--secret` は繰り返し指定可能です。`src` を省略すると id が既定になります。
- リモートビルド (`--builder`) では、シークレットは 9P/WebSocket でサーバーへストリームされ、レイヤーには決して入りません。

**関連ページ:** [cornus build](/ja/cli/build), [credentials](/ja/guides/credentials)

## SSH エージェントをビルドに転送する (--ssh)

`RUN --mount=type=ssh` のステップからローカル SSH エージェントにアクセスできるようにします。たとえばプライベートリポジトリを clone する場合に使います。

```sh
cornus build -t localhost:5000/app:latest --ssh default .
```

```dockerfile
RUN --mount=type=ssh git clone git@github.com:me/private.git
```

- `--ssh` は繰り返し指定可能で、`default` または `ID[=SOCKET]` を取ります。ソケットが見つからない場合は `$SSH_AUTH_SOCK` にフォールバックします。

**関連ページ:** [cornus build](/ja/cli/build)

## 名前付きビルドコンテキストを使う (--build-context NAME=パス)

追加のディレクトリをビルドに公開し、ステップが `from=NAME` でバインドマウントできるようにします。

```sh
cornus build -t localhost:5000/app:latest \
  --build-context data=./data .
```

```dockerfile
RUN --mount=type=bind,from=data,target=/data ./import.sh /data
```

- `--build-context` は繰り返し指定可能です。リモートビルドではディレクトリがサーバーへストリームされます (既定では先行、`--lazy` 付きでは遅延)。

**関連ページ:** [cornus build](/ja/cli/build)

## リモートサーバーでビルドし (--builder)、コンテキストを遅延にストリームする (--lazy)

リモート Cornus サーバー上でビルドを実行し、コンテキスト、ビルドコンテキストディレクトリ、シークレットを 9P/WebSocket でストリーミングします。

```sh
cornus build --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 \
  --build-context data=./big-data \
  --lazy ./context
```

- `--builder` は `ws://` / `wss://` または `http(s)://` base URL (env `CORNUS_BUILDER`) を受け取ります。サーバーを指す選択中の接続プロファイルもビルドをリモートに経路します。
- `--lazy` は `--build-context` dir を demand-driven に提供するため、ビルドが実際に読む byte だけが wire を渡ります。遅延は `containerd` ビルドワーカーでは対応されません。

**関連ページ:** [cornus build](/ja/cli/build), [リモート clusters](/ja/guides/remote-clusters), [リモート workflows](/ja/topics/remote-workflows)

## リモートビルドキャッシュをインポート / エクスポートする (--cache-to / --cache-from)

レジストリをバックエンドとするキャッシュを使って、マシンや CI の実行をまたいでビルドキャッシュを永続化し、再利用します。

```sh
cornus build -t localhost:5000/app:latest \
  --cache-to type=registry,ref=localhost:5000/app:cache \
  --cache-from type=registry,ref=localhost:5000/app:cache .
```

- どちらのフラグも繰り返し指定可能で、buildx-style 仕様を取ります。`type=local` の場合、`dest=` / `src=` 値はファイルシステムパスではなく engine-managed キーです (省略時は `--tag` から auto-derived)。そのためローカルビルドとリモートビルドで同じように動きます。

**関連ページ:** [cornus build](/ja/cli/build)

## クリーンビルドを強制する (--no-cache)

キャッシュ済みのレイヤーをすべて無視し、すべてのステップを最初から再ビルドします。

```sh
cornus build -t localhost:5000/app:latest --no-cache .
```

- ビルドを deterministic に再現したい場合や、upstream base-image が変わった後に使います。

**関連ページ:** [cornus build](/ja/cli/build)

## ルートレスでビルドする (--rootless)

ローカルビルドを root ではなくユーザー名前空間内で実行します。

```sh
cornus build -t localhost:5000/app:latest --rootless .
```

- `CORNUS_ROOTLESS` により server-wide にも設定できます。ホスト上で動作するルートレス user-namespace stack が必要です。

**関連ページ:** [cornus build](/ja/cli/build), [セキュリティ](/ja/guides/security)
