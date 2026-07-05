# SSH 経由のリモートコンテナホスト

リモートの **コンテナホスト** で直接動作する cornus サーバーに SSH トンネル経由で接続します。これは [リモートクラスター](/ja/guides/remote-clusters) (Kubernetes API を経由する) に対応するホスト向けの方法です。コンテキストを構成すれば、通常のコマンド (`deploy`、`compose`、`exec`、`build` など) はコマンドごとのフラグなしでトンネルを通ります。

このトンネルは **ローカルポートをバインドしません**。cornus は SSH 接続を介してリモートサーバーへ直接ダイヤルするため、ローカルマシンで待ち受けるものは残りません。

**このトンネルは接続先がどのランタイムを駆動するかを問いません。** トランスポートが運ぶのは cornus サーバーへの raw バイトであり、そのサーバーが何にデプロイするか (Docker、containerd、デーモンなしの OCI ランタイム、Incus) は *ホスト側* で `CORNUS_DEPLOY_BACKEND` を使って選ぶものです。ここで構成するコンテキストは何も変わりません。変わるのはそちらに何をインストールしておく必要があるかです。[ホストの前提条件](#ホストの前提条件) を参照してください。

SSH の接続先とリモートアドレスを選び、接続を検証し、ホスト用の systemd ユニットを生成する対話的な設定には、[`cornus setup`](/ja/cli/setup) ウィザードを実行してください。四つの SSH シナリオ (`ssh-docker`、`ssh-containerd`、`ssh-bare`、`ssh-incus`) はまったく同じ質問を行い、異なるのは表示されるセットアップ手順と、生成されるユニットが選ぶバックエンドだけです。

## コンテキストを設定する

ホストがすでに `~/.ssh/config` にあれば、エイリアスを指定するだけで cornus が残りを読み込みます (HostName、User、Port、IdentityFile、known_hosts、ProxyJump)。

```sh
cornus config set-context devbox --ssh-host devbox
cornus config use-context devbox
cornus compose -f compose.yaml up -d   # devbox 上でトンネル経由に実行される
```

ssh_config エントリーがない場合は、アドレスと資格情報を明示的に指定します。

```sh
cornus config set-context devbox \
  --ssh-host ssh.example.com:22 \
  --ssh-user ops \
  --ssh-identity-file ~/.ssh/id_ed25519
```

`cornus config get-contexts` は SSH トンネルのプロファイルを `(ssh-tunnel ops@ssh.example.com:22 -> 127.0.0.1:5000)` と表示します。

- `--ssh-remote-addr` は、リモートホストから見た cornus サーバーの待受先です (既定値は `127.0.0.1:5000`)。トンネルの出口はリモートホスト自身なので、既定の `:5000` に bind したサーバーへはそのループバックで到達できます。
- 明示した `--ssh-*` フラグは ssh_config から解決した値を上書きします。`--ssh-no-config` は ssh_config を完全に無視します。

## ホストの前提条件

ここまでで設定するのは手元側だけです。リモートホストでは、`cornus serve` が選んだバックエンドの必要とするものを揃えておく必要があります。しかもそのいずれも起動時には失敗しないため、どれかを欠いたサーバーは一見正常に見え、原因から何層も離れたところでデプロイが失敗して初めて分かります。

| `CORNUS_DEPLOY_BACKEND` | リモートホストに必要なもの |
| --- | --- |
| 未設定 (`dockerhost`) | Docker ソケット (`/var/run/docker.sock`、または `CORNUS_DOCKER_SOCK`)。 |
| `containerd` | root、containerd ソケット、そして `/opt/cni/bin` の CNI プラグイン (`bridge`、`portmap`、`host-local`、`loopback`)。 |
| `bare` | root、`PATH` 上の OCI ランタイム (既定は `runc`。`CORNUS_BARE_RUNTIME` で `crun`、`youki`、`runsc` を選べます)、そして同じ CNI プラグイン。デーモンは一切不要です。 |
| `incus` | `CORNUS_INCUS_SOCKET` (既定 `/var/lib/incus/unix.socket`) で到達できる incusd **6.3+**、加えて **デーモンホスト側に** インストールされた `skopeo` と `umoci`。incusd が OCI イメージを平坦化するためにこれらを呼び出すので、cornus が動くホストではなく incusd が動くホストに必要です。 |

`cornus serve` が起動時に実行するのと同じチェックをホスト自身で実行すれば、そのホストを使うと決める前に確認できます。

```sh
ssh devbox 'CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight'
```

`cornus serve` が起動を拒否する環境では終了ステータスが 0 以外になります。各バックエンドの機能と権限の全体像は [デプロイバックエンド](/ja/reference/deploy-backends) を参照してください。

## 認証

認証は OpenSSH に従います。

- ローカルの **ssh-agent** が既定で使われます。agent 内の鍵をホストが拒否し、"too many authentication failures" になる場合は `--ssh-no-agent` を渡してください。
- `--ssh-identity-file` は明示した鍵を追加します。パスフレーズで保護された鍵は最初のフォアグラウンド接続時に **一度だけ** プロンプトが表示されます。`SSH_ASKPASS` / `SSH_ASKPASS_REQUIRE`、続いて端末の順で使用します。再接続時には再度尋ねません。切断後も無人で維持するトンネルでは、鍵を ssh-agent に読み込んでください (agent は復号済みの鍵を保持しますが、cornus に復号済みのものは保存されません)。
- ホスト鍵検証は **fail-closed** です。cornus は `known_hosts` (`--ssh-known-hosts`、または ssh_config の `UserKnownHostsFile`、または `~/.ssh/known_hosts`) か、`--ssh-host-key` でピン留めした鍵を使います。`--ssh-insecure-host-key` は検証を無効にします (開発時のみ)。

## トンネル越しの TLS

SSH トンネルは生のバイト列を運ぶため、リモートサーバーが TLS を終端している場合は `--ssh-tls` でエンドツーエンドの HTTPS としてダイヤルできます。トンネルを通じたエンドポイントは `127.0.0.1:<port>` としてダイヤルされるので、検証を一致させるため証明書の実際のホスト名を cornus に指定してください。

```sh
cornus config set-context devbox --ssh-host devbox \
  --ssh-tls --tls-server-name cornus.internal.example.com
```

または、提示された証明書を信頼する CA を `--tls-ca-cert` で与えるか、開発時は `--insecure-skip-verify` を使用します。

## Bastion と ProxyCommand

`ProxyJump` (bastion チェーン) はネイティブにサポートされます。ホストエイリアスの ssh_config に設定すると、cornus が各ホップをプロセス内でダイヤルします。

```
Host devbox
  HostName 10.0.0.5
  User ops
  ProxyJump bastion.example.com
```

プロセス内のパスが実装していない `ProxyCommand` または `Match` ブロックでは、cornus はシステムの `ssh` バイナリにフォールバックします。持続する `ssh -N -L <unix-socket>:<remote>` を一つ実行し、その unix socket にダイヤルします (それでもローカル TCP ポートは使いません)。これはホストに `ProxyCommand` がある場合に自動で行われ、`--ssh-use-binary` で強制できます。`ssh` バイナリが必要で、Linux/macOS でのみ利用できます。

## 再接続

SSH 接続が切れた場合 (一時的なネットワーク障害、sshd の再起動、ホストの再起動)、cornus は必要に応じて再確立するため、次のコマンドは透過的に成功します。リンクが切れたとき **ストリームの途中** にあるコマンド (`logs -f`、対話的な `exec`、実行中のビルド) はエラーとして切断を通知します。リンクが復旧したらもう一度実行してください。

## レジストリに関する注意

リモートホストのレジストリが同じ SSH トンネル経由でしか到達できない場合、デプロイ先がプルできる明示的な `--registry` / `CORNUS_REGISTRY` を設定してください。イメージをプルするのは CLI のトンネル経由ではなくノード自身です。[イメージのビルド](/ja/guides/building-images) を参照してください。

**関連項目:** [リモートクラスター](/ja/guides/remote-clusters)、[cornus config](/ja/cli/config)、[デプロイバックエンド](/ja/reference/deploy-backends)。
