# cornus daemon

長時間動作するヘルパーデーモンです。クライアント側 Docker エンジン API プロキシ、クライアント側バックグラウンドエージェントの状態確認・停止操作、ホスト環境の preflight、Pod 向けサイドカーを含みます。

## 構文

```sh
cornus daemon <subcommand> [flags]
```

## 説明

`cornus daemon` はヘルパープロセスをまとめます。エンドユーザー向けサブコマンドは Docker エンジン API プロキシ (`docker`)、バックグラウンドエージェントの制御 (`status`、`stop`)、ホスト環境のチェック (`preflight`) です。残りのサブコマンドは生成された Pod 仕様に組み込まれる Pod サイドカーであり、手動では実行しません。Cornus サーバー自身は[`cornus serve`](/ja/cli/serve)です。

## cornus daemon docker

Unix ソケット上で Docker エンジン REST API の一部を提供するローカルデーモンを実行し、コンテナ操作をリモート Cornus サーバーに対する `cornus deploy` へ変換します。`DOCKER_HOST` をそのソケットに向けると、通常の `docker` がリモート Cornus 上でワークロードを実行します。呼び出し元のローカルバインドマウントディレクトリは 9P でストリーミングされます。

```sh
cornus daemon docker [flags]
```

フロントエンドは単一のクライアント側バックグラウンドエージェントがホストします (必要に応じて起動)。フォアグラウンド実行は `Ctrl-C` まで保持してからフロントエンドを解除します。`-d`/`--daemon` はフロントエンドを登録して戻り、エージェントがホストを継続します。

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--host` | `CORNUS_HOST` | `http://localhost:5000` | リモート cornus サーバー URL。選択中の接続プロファイル、次に既定値へフォールバックします。 |
| `--socket` | `CORNUS_DOCKER_SOCK` | `$XDG_RUNTIME_DIR/cornus-docker.sock` | listen する Unix ソケット。 |
| `-d`, `--daemon` | — | `false` | バックグラウンドデーモンとして実行します (既定はフォアグラウンド)。 |
| `--no-forward-ports` | — | `false` | コンテナポート (`docker -p`) をローカルリスナーへ公開しません。 |

これを使うと通常の `docker` / `docker compose` をリモート cornus サーバーに対して操作できます。組み込み Compose クライアントは[`cornus compose`](/ja/cli/compose)、リモート利用の全体像は[リモートクラスターで作業する](/ja/guides/remote-clusters)を参照してください。

::: warning 名前付きボリュームの削除
プロキシは、選択したデプロイバックエンドを通じて名前付きボリュームをプロビジョニングしますが、`docker volume rm` はこのプロキシプロセスのメモリから名前を削除するだけで、バックエンドのストレージは削除しません。`docker volume prune` と `docker system prune` のボリューム処理も、回収した容量を報告せず、同様にバックエンドのストレージを残します。データを削除する必要がある場合は、Cornus のバックエンド対応ボリュームライフサイクルを使用してください。
:::

## cornus daemon 状態

実行中の cornus クライアントエージェントの一覧 (servers、projects、docker frontends、conduit banners) を表示します。エージェントがない場合は、その旨を報告します。

```sh
cornus daemon status
```

## cornus daemon stop

実行中の cornus クライアントエージェントを停止します。

```sh
cornus daemon stop
```

## cornus daemon preflight

このプロセスが、設定されたデプロイバックエンドのコンテナランタイムを実際に駆動できるかどうかを確認します。

```sh
cornus daemon preflight
```

[`cornus serve`](/ja/cli/serve) が起動時に行うものとまったく同じ検出とチェックを実行するため、近似ではなく実際のサーバーについて答えます。またサーバーが起動を拒否する構成では **非ゼロ** で終了するため、イメージのスモークテストや CI ジョブのゲートに使えます。

要点は、デプロイを確定する *前* に実行することです。サーバーとして使う予定のコンテナイメージの中で、同じマウントと同じ環境で、バインドの変更がまだ容易なうちに実行します。

```
cornus runs in a container (a1b2c3d4e5f6) on a docker host; translating its paths for the runtime
  [ok  ] data-dir-host-visible: data dir /var/lib/cornus is /srv/cornus on the host
  [warn] client-local-mounts: client-local mounts unavailable: ...
           remedy: run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded
```

各行はチェック項目、その判定 (`ok`、`warn`、`fail`)、検出内容、そして対応が必要なものについては対処方法を示します。`warn` は機能が利用できないものの、何かがそれを要求したときに自身で不在を報告することを意味します。`fail` はデプロイが黙って誤動作することを意味します。`--output json` は同じ結果を 1 つのオブジェクトとして出力します。

これらを一切必要としない、ホスト上で直接動作するサーバーでは、出力はその旨を示す 1 行だけです。[サーバーをコンテナで実行する](/ja/guides/server-in-a-container) を参照してください。

## Pod サイドカーと内部サブコマンド

以下のサブコマンドはエンドユーザー向けではありません。生成される Pod 仕様にその呼び出し名が埋め込まれる、またはクライアントによって起動されるために存在します。

- `caretaker` — 削除まで設定済みロール (9P マウント、hub など) を実行する Pod サイドカー。
- `caretaker-check` — サイドカーの readiness probe。全 caretaker ロールが稼働中なら 0 で終了します。
- `net-redirect` — app エグレスを caretaker プロキシへ iptables リダイレクトする init コンテナ。

非表示の `mounts` と `agent` サブコマンドはクライアント側バックグラウンドエージェントの内部用です (`cornus compose up -d` のようなクライアントが起動し、手動では実行しません)。

## 例

Docker API プロキシをフォアグラウンドで提供し、`DOCKER_HOST` をエクスポートします。

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

カスタムソケットでプロキシを detach して実行します。

```sh
cornus daemon docker -d --socket /run/cornus-docker.sock
```

バックグラウンドエージェントを確認・停止します。

```sh
cornus daemon status
cornus daemon stop
```
