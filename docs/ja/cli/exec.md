# cornus exec

リモート Cornus サーバーを介して、デプロイメントの最初のインスタンス内でコマンドを実行します (docker exec)。

## 構文

```sh
cornus exec [flags] <name> -- <cmd> [args...]
```

## 説明

`cornus exec` はリモート Cornus サーバーに対する exec を作成・開始し、ローカルの標準入出力をデプロイメントの最初のインスタンスで実行されるコマンドへ接続します。デプロイメント名より後ろの内容はそのままコマンドへ渡されるため、`-c` のようなフラグも Cornus ではなくコマンドに届きます。

サーバーは `--server` / `CORNUS_SERVER` で選択し、指定がなければ選択中の接続プロファイルを使います ([`cornus config`](/ja/cli/config)を参照)。

`-i` ではローカル stdin をコマンドへ転送します。`-t` では pseudo-TTY を要求しますが、stdin 自体が端末の場合に限られます。パイプまたは CI からの起動は、クライアントが操作できないサーバー PTY にはせず、警告付きで通常ストリームへ切り替わります。TTY モードではローカル端末を生のモードで操作し、ウィンドウサイズ変更も転送します。

cornus はリモートコマンドの終了コードを自身の終了コードとして伝播します。コマンドは終了したものの終了状態を取得できない場合 (確認失敗) は `125` で終了します。これは「コマンドは実行されたがツール側が完了できなかった」ことを示す docker の慣例に従います。

`--forward-agent` はローカルの ssh-agent (`SSH_AUTH_SOCK`) を exec セッション内へ転送するため、セッション内で実行する `ssh` などのコマンドから agent が保持する鍵を使用できます。この転送には caretaker の `AgentRelayRole` を使い、バックエンドに応じて次の 2 通りで利用できます。

- **dockerhost / containerdhost**: リモートモードのバックエンド (`CORNUS_DOCKER_REMOTE` / `CORNUS_CONTAINERD_REMOTE`。[デプロイバックエンド](/ja/reference/deploy-backends)を参照) で利用できます。このモードがインスタンスごとにすでにプロビジョニングしている常駐 companion sidecar を経由します。同じホストにある (リモートではない) バックエンドに対して使用すると、明確なエラーで拒否されます。
- **kubernetes**: [DeploySpec](/ja/reference/deploy-spec) で `agentForward` を設定して適用したデプロイメントでのみ利用できます (Compose サービスでは `x-cornus-agent-forward: true` で設定)。kubernetes にはバックエンド全体の「リモートモード」がなく、この機能だけのためにすべてのデプロイメントで caretaker sidecar を動かすのは無駄になるため、デプロイメントごとの明示的なオプトインです。この設定なしで適用したデプロイメントに対して使用すると、明確なエラーで拒否されます。

`ssh -A` と同様、信頼できる Cornus サーバーに対してのみ使用してください。exec セッションが開いている間、サーバーは exec されたコマンド自身が発行したものに限らず、任意のチャレンジへの署名を転送先の agent に要求できます。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | selected プロファイル | リモート cornus サーバー URL (`http(s)://` または `ws(s)://`)。選択した接続プロファイルへフォールバックします。 |
| `-i`, `--interactive` | — | `false` | stdin を開いたままコマンドへ転送します。 |
| `-t`, `--tty` | — | `false` | pseudo-TTY を割り当てます (stdin が端末でない場合は通常ストリームへ切り替え)。 |
| `--forward-agent` | — | `false` | ローカルの ssh-agent を exec セッションへ転送します (リモートモードの dockerhost / containerdhost、またはデプロイメントに `agentForward` を設定した kubernetes)。 |
| `name` (位置引数) | — | 必須 | exec の対象となるデプロイメント名。 |
| `cmd...` (位置引数) | — | 必須 | 実行するコマンドと引数 (そのまま渡されます)。 |

## 例

一回限りのコマンドを実行します。

```sh
cornus exec myapp -- ls -la /app
```

対話シェルを開きます。

```sh
cornus exec -it myapp -- sh
```

明示的なサーバーを対象にします。

```sh
cornus exec --server https://cornus.example.com myapp -- env
```

## 関連項目

- [`cornus deploy`](/ja/cli/deploy)
- [`cornus config`](/ja/cli/config)
- [リモートクラスターで作業する](/ja/guides/remote-clusters)
