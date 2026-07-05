# cornus build

BuildKit ベースのエンジンでコンテキストからイメージをビルドし、レジストリへプッシュします。

## 構文

```sh
cornus build -t <ref> [flags] [context]
```

## 説明

`cornus build` はビルドコンテキストディレクトリ (位置引数、既定は `.`) から `-t/--tag` で指定したイメージをビルドします。既定ではこのホスト上のプロセス内ビルドエンジンを使用し、結果を対象レジストリへプッシュします。

`--builder`、またはサーバーを指定する選択済み接続プロファイルを使用すると、代わりにリモート Cornus サーバーでビルドが実行されます。このマシンはコンテキスト、`--build-context` ディレクトリ、シークレットを 9P/WebSocket でサーバーへストリーミングします。[リモートワークフロー](/ja/topics/remote-workflows)を参照してください。

リモートビルドでは、レジストリ部分を持たない `-t/--tag` (例: `app:v1`、`team/app:v1`) はサーバー組み込みレジストリで修飾されます。素のタグは*既定*レジストリを表し、Cornus の既定は Docker Hub ではなく自身のレジストリです。`--registry` / `CORNUS_REGISTRY` はこのホストを上書きします。未設定ならサーバーが広告するレジストリホスト、次に builder エンドポイントホストが既定です。すでにレジストリを含むタグ (例: `registry.example.com/app:v1`) はそのままで、純粋にローカルなプロセス内ビルドでは素のタグを Docker 自身の正規化に委ねます。

`--build-arg`、`--secret`、`--ssh`、`--build-context` はすべて繰り返し指定できます。`--cache-to` / `--cache-from` は buildx 形式のキャッシュ仕様を受け付けます。`type=local` の `dest=` / `src=` の値はファイルシステムパスではなくエンジン管理キーです。省略時には `--tag` から自動導出されます。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `-t`, `--tag` | — | 必須 | 対象イメージ参照。例: `localhost:5000/app:v1`。 |
| `context` (位置引数) | — | `.` | ビルドコンテキストディレクトリ。 |
| `-f`, `--file` | — | `Dockerfile` | コンテキストからの相対 Dockerfile パス。 |
| `--build-arg` | — | — | ビルド arg `KEY=VALUE`。繰り返し指定可。 |
| `--secret` | — | — | シークレットマウント `id=NAME,src=PATH` (`RUN --mount=type=secret`)。繰り返し指定可。`src` 省略時は id が既定です。 |
| `--ssh` | — | — | SSH エージェント転送: `default` または `ID[=SOCKET]` (`RUN --mount=type=ssh`)。繰り返し指定可。ソケットがなければ `$SSH_AUTH_SOCK` へフォールバックします。 |
| `--build-context` | — | — | 名前付きビルドコンテキスト `NAME=PATH` (`RUN --mount=type=bind,from=NAME`)。繰り返し指定可。 |
| `--builder` | `CORNUS_BUILDER` | — | リモート Cornus ビルドエンドポイント (`ws://` または `http(s)://` のベース URL)。設定時はそこでビルドし、このマシンがコンテキスト、ビルドコンテキストのディレクトリ、シークレットを 9P/WebSocket でストリーミングします。 |
| `--registry` | `CORNUS_REGISTRY` | derived | リモートビルドでレジストリ部分を持たない `--tag` に使うレジストリホスト。サーバー広告ホスト、なければ builder エンドポイントホストが既定です。 |
| `--rootless` | `CORNUS_ROOTLESS` | `false` | ビルドをルートレスモード (ユーザー名前空間) で実行します。 |
| `--lazy` | `CORNUS_LAZY_BUILD` | `false` | `--build-context` dirs を先行に同期せず、9P で必要に応じてに提供します (遅延ビルド)。サーバー全体の `CORNUS_LAZY_BUILD` でも有効になります。 |
| `--cache-to` | — | — | キャッシュエクスポートバックエンド (buildx 構文)。例: `type=registry,ref=HOST/app:cache[,registry.insecure=true]`。繰り返し指定可。 |
| `--cache-from` | — | — | キャッシュインポートバックエンド (buildx 構文)。例: `type=registry,ref=HOST/app:cache[,registry.insecure=true]`。繰り返し指定可。 |
| `--no-cache` | — | `false` | ビルドキャッシュを使用しません。 |
| `--no-push` | — | `false` | ビルドのみ行い、結果をプッシュしません。 |
| `--insecure` | — | `true` | HTTP (非 TLS) レジストリへのプッシュを許可します。 |

## 例

ローカルイメージをビルドしてプッシュします。

```sh
cornus build -t localhost:5000/app:v1 .
```

別の Dockerfile とビルド引数でビルドします。

```sh
cornus build -t localhost:5000/app:v1 -f docker/Dockerfile --build-arg VERSION=1.2.3 .
```

シークレットを渡し、SSH エージェントを転送します。

```sh
cornus build -t localhost:5000/app:v1 \
  --secret id=npmrc,src=$HOME/.npmrc \
  --ssh default .
```

リモート Cornus ビルダーでビルドします。

```sh
cornus build -t registry.example.com/app:v1 --builder wss://build.example.com .
```

レジストリキャッシュをエクスポート・インポートします。

```sh
cornus build -t localhost:5000/app:v1 \
  --cache-to type=registry,ref=localhost:5000/app:cache \
  --cache-from type=registry,ref=localhost:5000/app:cache .
```

## 関連項目

- [リモートワークフロー](/ja/topics/remote-workflows)
- [`cornus push`](/ja/cli/push)
- [クイックスタート](/ja/introduction/quick-start)
