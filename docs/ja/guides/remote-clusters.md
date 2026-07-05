# リモートクラスターで作業する

ファイル、シークレット、Kubernetes へのアクセス権をローカルに保ったまま、自分のマシンからリモート Cornus サーバーを操作するためのタスク指向レシピです。使用可能なフィールドの一覧は、[接続設定](/ja/reference/connection-config)と[リモートワークフロー](/ja/topics/remote-workflows)を参照してください。

## 1 回限りのコマンドでリモートサーバーを指定する

プロファイルを作成せずに、単一のコマンドの接続先サーバーを指定します。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_SERVER=https://cornus.example.com CORNUS_TOKEN="$TOKEN" cornus exec -it web -- sh
```

- `--server` は `CORNUS_SERVER` より優先し、`CORNUS_SERVER` は選択中プロファイルより優先します。エンドポイントは `http(s)://` または `ws(s)://` を受け付けます。
- ベアラートークンは `CORNUS_TOKEN` (またはプロファイル) から読み取られます。コマンドフラグとして指定することはできません。

**関連項目:** [リモートワークフロー](/ja/topics/remote-workflows)、[cornus deploy](/ja/cli/deploy)

## リモートサーバー用の接続プロファイルを作成する

サーバー URL、トークン、TLS 関連情報を一度保存すれば、コマンドラインでの指定は不要です。

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod
cornus deploy -f app.yaml
```

- `set-context` は既定で名前付きコンテキストを置き換えます。`--merge` を渡すと、未指定のフィールドを維持したまま直接編集します。
- 設定の重ね合わせ順は、`--from-file` (基底)、フラグ、`--from-file-override` (最上位) です。

**関連項目:** [cornus config](/ja/cli/config)、[接続設定](/ja/reference/connection-config)

## プロファイル経由でクラスター内サーバーへ自動ポート転送する

URL ではなく Service を指定することで、イングレスのないクラスター内の Cornus に接続できます。CLI は各コマンドの実行中にポート転送を開きます。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster
cornus compose ps     # svc/cornus へ透過的にポート転送して接続する
```

- `--server` は未設定のままにします。空の `server` に `port-forward` ブロックがあれば、クラスター内の Service へ接続します。
- `--pf-kube-context` は kubeconfig のコンテキストを選択します。`--pf-service` を設定すると Service の自動検出を省略します。

**関連項目:** [リモートワークフロー](/ja/topics/remote-workflows)、[cornus config](/ja/cli/config)

## 自分の Kubernetes へのアクセス権から短命な資格情報を発行する

静的トークンを保存する代わりに、Kubernetes TokenRequest API を介してクラスターの ServiceAccount からベアラートークンを取得します。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # クラスタートークンを発行してポート転送する。静的トークンは不要
```

- `--kube-auth-audience` はサーバーの `CORNUS_JWT_AUDIENCE` と一致しなければなりません。
- `--kube-auth-namespace` / `--kube-auth-kube-context` の既定値は `--pf-*` の値です。`--kube-auth-expiration-seconds` の既定値は `3600` です。

**関連項目:** [接続設定](/ja/reference/connection-config)、[認証と TLS](/ja/topics/auth-and-tls)

## プロファイルを切り替え、表示し、削除する

kubeconfig と同様の形式で接続プロファイルの集合を管理します。

```sh
cornus config get-contexts          # プロファイルを一覧表示する (現在のものには * が付く)
cornus config use-context staging   # staging を既定にする
cornus config current-context       # 現在のコンテキスト名を表示する
cornus config view                  # ファイルを表示する (トークンは伏せ字)
cornus config delete-context old    # プロファイルを削除する
```

- `view --show-tokens` はベアラートークンを表示します。`view --export --context prod` は、`set-context --from-file` で再び読み込める単一の Context オブジェクトを出力します。
- `delete-context` は、削除したコンテキストを指していた場合に current-context の参照を解除します。

**関連項目:** [cornus config](/ja/cli/config)

## プロファイルに既定名前空間を設定する

クラスターの検出と kube-auth の既定値に使う、Cornus のインストール先名前空間を記録します。

```sh
cornus config set-context staging -n cornus-system
```

- `-n`/`--namespace` は、`--pf-service` または `--no-detect` が設定されない限り Service とポートを自動検出します。クラスターに接続せず名前空間だけを保存するには `--no-detect` を追加します。

**関連項目:** [cornus config](/ja/cli/config)、[接続設定](/ja/reference/connection-config)

## クライアントからワークロードへの通信をサーバー経由にする

kubeconfig で Pod に直接接続するのではなく、ログとポート転送で Cornus サーバープロキシを経由するよう強制します (クラスタープロファイルのみ)。

```sh
cornus config set-context cluster --merge --via-server
cornus port-forward --via-server web 8080:80    # コマンド単位の上書き
```

- 優先順位は、コマンドごとの `--via-server` / `--no-via-server` フラグ、`CORNUS_VIA_SERVER` (`1`/`0`)、プロファイルのフィールドです。
- 変更されるのは転送経路だけです。`kube-auth` プロファイルは引き続きクラスタートークンを発行します。

**関連項目:** [リモートワークフロー](/ja/topics/remote-workflows)、[cornus port-forward](/ja/cli/port-forward)

## リモートデプロイメントのログを追跡し、コマンドを実行する

解決済みのサーバーまたはプロファイルを通じてワークロードのログをストリームし、その中でコマンドを実行します。

```sh
cornus compose logs --follow --tail 100 web
cornus exec -it web -- sh
```

- クラスタープロファイルでは、ログと exec は kubeconfig を使って Pod へ直接接続し、失敗時にはサーバープロキシへフォールバックします。`--via-server` はサーバー経由の経路を強制します。
- `--` の後ろに置いたすべては、`exec` ではコマンドにそのまま渡されます。stdin が端末でなければ `-t` はプレーンストリームに切り替わります。

**関連項目:** [cornus exec](/ja/cli/exec)、[cornus compose](/ja/cli/compose)
