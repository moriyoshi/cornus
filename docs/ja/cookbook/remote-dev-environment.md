# クラスター上のリモート開発環境

## シナリオ

軽量なノート PC で開発しているものの、コードには強力なマシンが必要だとします。大規模なビルド、GPU、冷却ファンを全開にするデータベースなどです。ファイルは自分のエディターで*ローカル*に編集しながら、コードはクラスター上で*リモート*に実行し、ワークロードのポートには `localhost` で到達でき、普段の Docker / Dev Container ツールもそのまま使いたい場合に適しています。Cornus はリモートサーバーをローカルに感じさせます。[接続プロファイル](/ja/reference/connection-config)がエンドポイントの設定を省き、[クライアントローカルバインドマウント](/ja/guides/deploying-workloads#クライアントローカルディレクトリをリモートワークロードにマウントする-local-mount、9p-でストリーム)が作業ツリーを 9P でストリームするため、コピーなしで編集内容が同期され、公開ポートは自動的に自分のマシンへ転送されます。

## 使用するもの

- [接続プロファイル](/ja/reference/connection-config) — [`cornus config`](/ja/cli/config)でサーバーを一度保存します。
- [`cornus compose`](/ja/cli/compose) — Compose プロジェクト (または [開発コンテナ](/ja/guides/compose-devcontainers-docker)) をサーバーに対して起動します。
- [9P 経由のクライアントローカルバインドマウント](/ja/guides/deploying-workloads#クライアントローカルディレクトリをリモートワークロードにマウントする-local-mount、9p-でストリーム) — ソースはノート PC に残り、リモートワークロードへ必要に応じてストリームされます。
- [自動ポート転送](/ja/guides/deploying-workloads) — セッションの存続期間、公開ポートは `127.0.0.1:<host>` で応答します。
- [`cornus daemon docker`](/ja/cli/daemon) — 任意で `DOCKER_HOST` を提供し、公式 `devcontainers` CLI (または通常の `docker`) からリモートサーバーを操作します。

## 手順

**1. クラスターをプロファイルとして保存します。** すべてのコマンドで `--server` やトークンが不要になります。イングレスのないクラスター内 cornus ではサービスを指定し、CLI がコマンドごとにそこへポート転送します。

```sh
cornus config set-context devbox \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context devbox
```

URL を持つサーバーでは代わりに `--server https://cornus.example.com --token "$(cat token.jwt)"` を使います。`--kube-auth-*` フラグは自身の Kubernetes へのアクセス権から短命トークンを発行するため、静的シークレットを管理する必要がありません。[リモートクラスター](/ja/guides/remote-clusters)を参照してください。

**2. 環境を Compose プロジェクトとして記述します。** `volumes:` 下のバインドマウントは*自分の*ノート PC 上のパスです。`ports:` は `localhost` で到達したいポートです。

```yaml
name: devbox

services:
  app:
    build: .                      # built by the cornus engine, pushed to its registry
    command: ["npm", "run", "dev"]
    working_dir: /workspace
    volumes:
      - ./:/workspace             # client-local: streamed over 9P, edits sync live
    ports:
      - "3000:3000"               # dev server, reachable at 127.0.0.1:3000
    environment:
      NODE_ENV: development
    depends_on:
      - db

  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: dev
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  pgdata:                         # named: shared/persistent across up/down
```

**3. フォアグラウンドで起動します。** サーバーで必要なものをビルドし、依存関係順にデプロイして、バインドマウントを 9P で維持し、`3000` と `5432` を `127.0.0.1` へ自動転送し、ログをストリームします。

```sh
cornus compose up --build
```

**4. ローカルで編集し、リモートで実行します。** エディターはノート PC の `./src/...` に書き込み、`app` コンテナは 9P マウントを通じて変更を確認し、開発サーバーが再読み込みします。`http://localhost:3000` を開くと、要求はワークロード Pod へトンネルされます (クラスタープロファイルでは kubeconfig を使って Pod へ直接接続し、それ以外ではサーバーを経由します)。`psql -h 127.0.0.1 -p 5432` も同じ方法でリモートデータベースへ到達します。`Ctrl-C` を押すと、`up` が起動したものを削除します。

**5. 公開されていないポートにも、仕様を編集せず必要に応じて到達します。**

```sh
cornus port-forward app 9229:9229     # e.g. a debugger port
```

## 仕組み

各要素が組み合わさることで、開発ループは変わりません。**接続プロファイル**は、エンドポイント、認証、ここではクラスター内ポート転送の対象を持つ CLI 側の kubeconfig 形式ファイルです。そのため、どの `cornus compose` 実行でもコマンドラインの指定なしにサーバーを解決します。**クライアントローカルバインドマウント**がローカル編集の鍵です。ホストパスを持つ Compose の `volumes:` エントリーは自分のマシンから 9P でストリームされ、セッションの存続期間、サーバー自身のマウント領域から提供されます。ワークロードはファイルをその場で読み取ります。事前コピーも rsync も不要で、ホスト権限ポリシーを緩めずに常にマウントを許可できます。**公開ポート**は Kubernetes バックエンドでも `127.0.0.1:<host>` へ自動転送されるため、リモートワークロードは `docker compose` と同じようにローカルで応答します。三つは実行中のフォアグラウンド `up` に結び付いています。切り離した `up -d` はマウントと転送をバックグラウンドクライアントエージェントに渡します (`cornus daemon status` で確認できます)。詳しくは[リモートクラスターで作業する](/ja/guides/remote-clusters)と[ワークロードをデプロイする](/ja/guides/deploying-workloads)のレシピを参照してください。

## バリエーション

**Compose ファイルの代わりに Dev Container を使う。** リポジトリに `.devcontainer/devcontainer.json` があれば、`cornus compose` は手書きの Compose ファイルなしでネイティブに読み取ります。ライフサイクルフック (`initializeCommand` はホストで、`postCreate` / `postStart` / `postAttach` はコンテナ内で実行) を実行し、プロジェクトを `workspaceFolder` に 9P でバインドマウントします。

```sh
cornus compose --devcontainer . up
```

**VS Code または Zed でリモートサーバー上の Dev Container を開く。** クライアント側の Docker Engine API プロキシを実行し、`DOCKER_HOST` をそれに向けます。通常の `docker`、`docker compose`、公式 `devcontainers` CLI、エディターの Dev Container 対応はすべてリモートの Cornus 上でコンテナを実行し、ローカルのバインドマウントディレクトリは 9P でストリームされます。

```sh
cornus daemon docker -d
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/cornus-docker.sock"
devcontainer up --workspace-folder .      # official CLI, remote execution
```

プロキシは Docker の正確なプロトコル (create/start、attach、wait、ライフサイクルイベントストリーム) を扱うため、VS Code の Dev Containers 拡張機能のエンジンである公式 `@devcontainers/cli` が変更なしに操作できます。同じシェルからエディターを起動して `DOCKER_HOST` を継承させ、通常の Dev Container の手順を使えば、コンテナはリモートで実行されます。

- **VS Code** — Dev Containers 拡張機能をインストールし、`code .` を実行して **Dev Containers: Reopen in Container** を選びます。
- **Zed** — `zed .` を実行してプロジェクトの Dev Container を開きます。Zed は同じ Docker エンドポイントを通じて起動します。

プロキシは Docker の `/build` エンドポイントをエミュレートしません (ビルドは [`cornus build`](/ja/cli/build)の役割です)。そのため、事前にビルドした `image:` を `devcontainer.json` で参照し、`build:` / `dockerFile:` は使わないでください。Dockerfile 由来なら先に `cornus build -t <registry>/devcontainer:latest .` でビルドしてください。

**一つのプロキシでサービス名を使ってすべてのサービスに到達する。** プロファイルのコンジットを SOCKS5 に設定すると、単一のスプリットトンネルプロキシが `app`、`db`、その他のサービスに名前で到達し、それ以外のトラフィックは直接エグレスします。

```sh
cornus config set-context devbox --merge --conduit-mode socks5
```

**関連項目:** [リモートクラスター](/ja/guides/remote-clusters) · [Compose、devcontainers、docker CLI](/ja/guides/compose-devcontainers-docker) · [接続設定](/ja/reference/connection-config) · [クックブック](/ja/cookbook/)
