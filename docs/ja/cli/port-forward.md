# cornus port-forward

一つ以上のローカルポートをデプロイメントのコンテナポートへ転送します。ホストへ公開されていない、またはサービス経由で公開されていないポートにも到達できます。

## 構文

```sh
cornus port-forward [flags] <name> <ports...>
```

## 説明

`cornus port-forward` は対応付けごとにローカルリスナーをバインドし、受け付けた接続をそれぞれ独立したトンネルでデプロイメントの最初のインスタンスへ転送します。`kubectl port-forward` と似ています。`Ctrl-C` (または `SIGTERM`) までフォアグラウンドで動作します。

クラスター接続プロファイルでは、kubeconfig 資格情報を使い Kubernetes `pods/portforward` SPDY subresource 経由で各接続をワークロード Pod へ直接トンネルします (通常サーバーの ServiceAccount にはできません)。直接の試行でトンネルを開けない場合だけサーバープロキシへフォールバックします。非クラスターのプロファイルでは cornus サーバーを経由し、サーバーがコンテナへ接続します。どちらでも、ホストに公開されていない、またはサービスに露出していないポートへ到達します。

各ポート mapping は `LOCAL:REMOTE` です。同じローカル・コンテナポートには `PORT` だけでも指定できます。Compose ポート記法で `/tcp` または `/udp` 接尾辞を任意で付けられます (既定は `tcp`)。例は `5353:53/udp` です。ポートは `1..65535` でなければなりません。`/udp` mapping は byte ストリームではなく datagram を転送し、dockerhost、containerd、bare バックエンドでサポートされます。Kubernetes ポート転送は TCP 専用なので、その mapping は警告とともにスキップされます。

接続は `--server`、または選択中の接続プロファイルから解決されます ([`cornus config`](/ja/cli/config)を参照)。ポート転送がワークロードへの到達手段全体でどの位置にあるかは[ネットワークと conduit](/ja/guides/networking)を参照してください。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | リモート cornus サーバー URL (`http(s)://` または `ws(s)://`)。選択した接続プロファイルへフォールバックします。 |
| `--address` | — | `127.0.0.1` | リスナーをバインドするローカルアドレス。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | プロファイル | kubeconfig で Pod へ直接接続する代わりに、cornus サーバープロキシを経由して転送します (クラスタープロファイルのみ)。`--no-via-server` は直接経路を強制します。`CORNUS_VIA_SERVER` とプロファイルを上書きします。 |

位置引数:

- `<name>` — 転送先デプロイメント名 (必須)。
- `<ports...>` — 一つ以上の `LOCAL:REMOTE[/tcp|/udp]` mapping、または `PORT` (必須)。

## 例

ローカルポート 8080 をコンテナポート 80 へ転送します。

```sh
cornus port-forward web 8080:80
```

すべてのインターフェースにバインドして複数のポートを同時に転送します。

```sh
cornus port-forward --address 0.0.0.0 web 8080:80 5432:5432
```

UDP ポートを転送します (dockerhost / containerd バックエンド)。

```sh
cornus port-forward dns 5353:53/udp
```

Pod への直接経路ではなくサーバープロキシ経路を強制します。

```sh
cornus port-forward --via-server web 8080:80
```
