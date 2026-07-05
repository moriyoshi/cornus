# Compose、Dev Container、Docker CLI

Docker 互換の機能向けレシピです。組み込みの [cornus compose](/ja/cli/compose) クライアント、Dev Container 対応、そして標準の `docker` CLI を [cornus daemon docker](/ja/cli/daemon) 経由で動かす方法を扱います。いずれもサーバーは `--host` / 接続プロファイル / `http://localhost:5000` から解決されます。

## Compose プロジェクトを起動して停止する (cornus compose up / down)

必要に応じてビルドとデプロイを行い、フォアグラウンドでログをストリーミングします。その後、プロジェクトを削除します。

```sh
cornus compose up
# Ctrl-C で停止するか、別の端末から実行する:
cornus compose down
```

- フォアグラウンドの `up` はクライアントローカルマウントと自動転送済みポートを保持し、Ctrl-C まで動き続けます。その後、自分が起動したものを削除します。`down` はサービスを依存関係の逆順で停止します。プロジェクトスコープの名前付きボリュームも削除するには `--volumes` を追加します。
- Compose ファイルの検出では、作業ディレクトリの `compose.yaml` / `compose.yml` / `docker-compose.yaml` / `docker-compose.yml` を探します。

**関連ページ:** [cornus compose](/ja/cli/compose), [deploying workloads](/ja/guides/deploying-workloads)

## プロジェクトの状態を確認する (cornus compose ps / logs)

サービスとその状態を一覧し、ログをストリームします。

```sh
cornus compose ps
cornus compose logs --follow --tail 100 web
```

- `ps` は `--format table|json`、`-q`、`--services` を取ります。`logs` は選択されたすべてのサービスを並行してストリームします。`-f` は `--follow` の短縮形ではありません。`-f` はグループがすでに `--file` 用に使います。
- クラスタープロファイルの場合、ログは kubeconfig を使って pod から直接読み取られ、必要な場合だけサーバープロキシにフォールバックします。

**関連ページ:** [cornus compose](/ja/cli/compose)

## up の間にイメージをビルドする (cornus compose up --build、--ssh 付き)

開始前にサービスイメージをビルドし、必要なビルドステップに SSH エージェントを転送します。

```sh
cornus compose up --build --ssh default
```

- `--build` は開始前にすべてのイメージをビルドします (ビルドサービスは常にビルドされます)。`--ssh` は `default` または `id[=socket]` を取り、各サービスの `build.ssh` の上に統合されます。
- 開始せずにビルドするには、`cornus compose build [--no-cache] [--build-arg KEY=VALUE]` を使います。

**関連ページ:** [cornus compose](/ja/cli/compose), [building images](/ja/guides/building-images)

## 複数の compose ファイル、env ファイル、プロファイルを使う (-f, --env-file, --profile)

複数の Compose ファイルを統合し、特定の環境変数ファイルを指定し、プロファイル付きサービスを有効化します。

```sh
cornus compose \
  -f compose.yaml -f compose.prod.yaml \
  --env-file .env.prod \
  --profile debug up
```

- これらはすべての subcommand に適用される group フラグです。`-f` は繰り返し指定可能で layered です。`--env-file` は既定の `.env` discovery を置き換えます (後のファイルが優先され、プロセス環境は引き続きそれらを上書きします)。`--profile` は繰り返し指定可能で、`COMPOSE_PROFILES` も尊重します。

**関連ページ:** [cornus compose](/ja/cli/compose)

## バックグラウンドエージェントで切り離して実行する (cornus compose up -d)

クライアントローカルマウント、forwarded ポート、SOCKS5、中継型エグレスをバックグラウンドエージェントに渡し、すぐに戻ります。

```sh
cornus compose up -d
# later:
cornus compose down
```

- `-d` / `--detach` はマウント、forwarded ポート、任意の SOCKS5 プロキシ、`proxy` / `transparent` エグレスセッションをクライアント側バックグラウンドエージェントに渡して戻ります。後で `down` により停止します。エージェントの確認や停止には `cornus daemon status` / `cornus daemon stop` を使います。
- ファイルをソースとする Compose の `configs:` と `secrets:` は、単一ファイルのクライアントローカルマウントです。dockerhost では親ディレクトリとサブパスを使って実現できます。Kubernetes の共有 9P サイドカーマウントは任意のルートファイルシステム上の対象へ 1 ファイルだけを投影できないため、これらを拒否します。ディレクトリのバインドマウントは Kubernetes でも引き続き利用できます。containerd バックエンドは現在、クライアントローカル deploy マウントをサポートしていません。

**関連ページ:** [cornus compose](/ja/cli/compose), [cornus daemon](/ja/cli/daemon)

## サービスを再ビルド、再起動、停止、開始する

down と up を完全にやり直さず、イメージを再ビルドしたり実行中サービスを再起動したりします。

```sh
cornus compose build web          # rebuild one service's image
cornus compose restart web        # restart in forward dependency order
cornus compose stop web           # stop in reverse dependency order
cornus compose start web          # start in forward dependency order
```

- `restart` / `stop` / `start` はそれぞれ任意のサービス list を取ります (既定: all)。バックグラウンドの `up -d` helper がクライアントローカルマウントを保持しているサービスは拒否されます。停止するには `down` を使ってください。

**関連ページ:** [cornus compose](/ja/cli/compose)

## Dev Container を実行する (cornus compose --devcontainer、または自動検出した .devcontainer)

devcontainer の定義を起動し、そのライフサイクルフックを実行します。

```sh
# Explicit path or search directory:
cornus compose --devcontainer .devcontainer up
# Or auto-detected when no Compose file is present:
cornus compose up
```

- devcontainer は、`--devcontainer` を指定した場合、`-f` 引数が `devcontainer.json` を指す場合、または Compose ファイルがなく `.devcontainer/devcontainer.json` (または `.devcontainer.json`) が自動検出できる場合に使われます。混在リポジトリでは Compose ファイルが常に優先されます。
- ライフサイクル hook が実行されます。コンテナの前にホスト上で `initializeCommand` が実行され、その後コンテナの起動に合わせてサービスごとの `postCreate` / `postStart` / `postAttach` が実行されます。

**関連ページ:** [cornus compose](/ja/cli/compose)

## 標準 docker CLI を Cornus サーバーに向ける (cornus daemon docker + DOCKER_HOST)

Docker エンジン API を話し、コンテナ operation を cornus deployに変換するローカルプロキシを実行してから、標準 `docker` をそこへ向けます。

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

- フォアグラウンド run は Ctrl-C まで保持します。`-d` / `--daemon` は frontend をバックグラウンドエージェントに登録して戻ります。ソケットの既定は `$XDG_RUNTIME_DIR/cornus-docker.sock` です (`--socket` / `CORNUS_DOCKER_SOCK` で上書き)。
- 呼び出し元のローカル bind-mount ディレクトリは 9P でサーバーへストリームされます。

**関連ページ:** [cornus daemon](/ja/cli/daemon), [リモート workflows](/ja/topics/remote-workflows)

## merged 設定を描画する / version を出力する (cornus compose 設定 / version)

プロジェクトについて cornus が解析 / 統合した view を確認するか、Compose CLI version を出力します。

```sh
cornus compose config              # full merged model as YAML
cornus compose config --services   # just service names, in dependency order
cornus compose version --short
```

- `config` は `--volumes`、`--images`、`--format yaml|json`、`-q` (検証のみ、何も出力しない) も取ります。`version` は `--short` または `--format pretty|json` を取ります。

**関連ページ:** [cornus compose](/ja/cli/compose), [cornus version-health](/ja/cli/version-health)
