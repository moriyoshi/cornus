# デプロイバックエンド

cornus deployエンジンは [デプロイスペック](/ja/reference/deploy-spec) (ネイティブ `deploy.yaml`、または Compose ファイル / devcontainer から変換されたもの) を、**交換可能な四つのバックエンド** のいずれかへ適用します。すべて同じインターフェースの背後にあり、`CORNUS_DEPLOY_BACKEND` 環境変数で選びます (環境変数のみ。CLI フラグはありません)。

| `CORNUS_DEPLOY_BACKEND` | 対象 | Networking | 備考 |
| --- | --- | --- | --- |
| `dockerhost` (既定) | ローカル Docker デーモン | Docker ネットワーク | Docker ソケット (`/var/run/docker.sock`) が必要。 |
| `containerd` | dockerd のない素の containerd ホスト | CNI bridge + portmap | Linux 専用。root + CNI プラグインが必要。 |
| `bare` | OCI ランタイム CLI (runc/crun/youki) を直接 — **デーモンなし** | CNI bridge + portmap | Linux 専用。root + OCI ランタイムバイナリ + CNI プラグインが必要。イメージプル・監督・cgroup を cornus 自身が所有。 |
| `kubernetes` / `k8s` | Kubernetes クラスター (client-go) | デプロイメント + サービス | サーバー / クラスター内のみ。RBAC 範囲。 |

選択はサーバー (`cornus serve`) と、`--server` なしで実行するローカル [`cornus deploy`](/ja/cli/deploy) の両方に適用されます。例外は `kubernetes` です。これは server/cluster 内専用で、`CORNUS_DEPLOY_BACKEND=kubernetes` のローカル `cornus deploy` は警告とともに `dockerhost` へフォールバックします。

四つすべてが同じ核となる仕様フィールド (`name` / `image` / `replicas` / `restart` / `env` / `ports` / `mounts`)、クライアントローカル 9P バインドマウント、Compose user ネットワーク、公開ポート転送を尊重します。そのため同じワークフローを変更せず移動できます。特定バックエンドにしか対応しない仕様フィールドは[デプロイスペックリファレンス](/ja/reference/deploy-spec)にフィールドごとに記載されています。

privilege の扱いは**default-deny**です。明示的に許可 (`CORNUS_ALLOW_PRIVILEGED`、`CORNUS_ALLOW_BIND_SOURCES`) しない限り、特権コンテナとホストバインドマウントは拒否されます。[認証と TLS](/ja/topics/auth-and-tls)を参照してください。

## `dockerhost` (既定)

ローカル Docker デーモン上でワークロードをコンテナとして実行します。Docker ソケット (`/var/run/docker.sock`、`CORNUS_DOCKER_SOCK` で上書き可) が必要です。最も機能が豊富なバックエンドで、仕様フィールドの最大範囲を Docker の create-time / host-config オプションに直接 map します。Compose user ネットワークは実際の Docker user-defined ネットワークとなり、libnetwork が DNS とネットワークごとの isolation をネイティブに提供します。

[host-native 再エクスポート](/ja/reference/server-env-vars#reusing-a-local-image-store) (このバックエンドでは既定) では、デーモンが既に持っているイメージ (bare またはループバックホストの参照) について、このバックエンドは**レジストリプルをスキップ**します。そのイメージをプルすると cornus のレジストリ経由で同じデーモンへ往復してしまうためです。外部の参照 (例: `docker.io/...`) は従来どおりプルされます。

## `containerd`

`CORNUS_DEPLOY_BACKEND=containerd` は **dockerd のない素の containerd ホスト上でネイティブに** ワークロードを実行し、containerd v1 クライアントに直接デプロイインターフェース全体を実装します。**Linux 専用**で、ほかの環境では unsupported エラーを返します。`dockerhost` と同様、サーバーに対してもサーバーなしのローカル `cornus deploy` に対しても動作します。

必要なもの:

- containerd ソケット (`CORNUS_CONTAINERD_ADDRESS`、既定 `/run/containerd/containerd.sock`; 標準 `CONTAINERD_ADDRESS` はフォールバックとして尊重)、
- **root** (ネットワーク名前空間を作成し CNI プラグインを実行するため)、
- 標準 CNI プラグイン (`bridge`、`portmap`、`host-local`、`loopback`)。`CORNUS_CNI_BIN_DIR`、`CNI_PATH`、`/opt/cni/bin` から検出します。

ワークロードは `cornus` containerd 名前空間 (`CORNUS_CONTAINERD_NAMESPACE`) に置かれ、バックエンド状態 (ボリューム、ログ、CNI 設定) は `<DataDir>/containerd/` 下に保存されます。

- **Networking** は host-port publishing を portmap で行う単純な CNI bridge です。Compose ネットワークごとに `CORNUS_CNI_SUBNET_BASE` (既定 `10.4`) から独自の `/24` を割り当てます。公開ポートはレプリカ 0 だけに DNAT されます。コンテナ間の名前解決は hosts-file 同期 (nerdctl 風) で機能します。UDP ポート mapping はサポートされます (kubernetes バックエンドと異なります)。
- **イメージプル** はプレーン HTTP と TLS を自身で選びます。`localhost` レジストリは自動的にプレーン HTTP、`CORNUS_CONTAINERD_INSECURE_REGISTRIES` (comma-separated `host[:port]`) は明示ホストに拡張します。`CORNUS_CONTAINERD_SNAPSHOTTER` は rootfs snapshotter を上書きします (docker-in-docker のような overlay-backed ホストでは `native` を設定)。
- **ログ** はデータディレクトリ下に保存され、`CORNUS_CONTAINERD_LOG_MAX_BYTES` (既定 16 MiB、旧世代一つを保持) で rotate され、cornus restart 後も残ります。**再起動ポリシー** は containerd restart-monitor プラグインに委譲されます。

containerd **ビルドワーカー** (`CORNUS_BUILD_WORKER=containerd`) と組み合わせると、ビルドの実行、snapshot、content が同じホスト containerd に委譲されます。タグ付きビルドはホストイメージストアに直接入り、ビルド直後のイメージをレジストリ往復なしでデプロイできます。遅延 build-context (`--lazy` / `CORNUS_LAZY_BUILD`) は containerd ワーカーでは**未対応**です。

**`dockerhost` との既知の差:** attach は output-only、ヘルスチェックは無視され警告が出ます。ルートレス containerd は現在未検証・未対応です。

## `bare`

`CORNUS_DEPLOY_BACKEND=bare` はワークロードを**デーモンレス**に実行します。dockerd も containerd もありません。cornus は低レベル **OCI ランタイム CLI** (`runc`、または `CORNUS_BARE_RUNTIME` 経由で `crun`/`youki`/`runsc`) を直接駆動し、デーモンが本来提供するすべてを自身で所有します。プロセス内 content store へのイメージプル、layer 展開 + rootfs 構築、OCI `config.json` 生成、**プロセス監督 + restart policy**、cgroup ライフサイクル、ロギングです。実質的に **cornus 自身が Podman になる** 構成です。ほかの host バックエンドと同様 **Linux 専用**で、サーバーにもローカル `cornus deploy` にも動作します。状態は `<DataDir>/bare/` 下に保存されます。

必要なもの:

- **root** (snapshotter mount、ネットワーク名前空間、CNI プラグイン、container cgroup のため)、
- `PATH` 上の **OCI ランタイムバイナリ** (既定 `runc`。起動時に検証され、欠落は実行可能なエラーで即座に失敗)、
- 標準 **CNI プラグイン** (`bridge`、`portmap`、`host-local`、`loopback`。`CORNUS_CNI_BIN_DIR`、`CNI_PATH`、`/opt/cni/bin` から検出)。

Networking、hosts-file 名前解決、DataDir ボリュームの挙動は `containerd` バックエンドと**まったく同じ**です。daemon 非依存の機構は共有コードです (CNI bridge + portmap、compose ネットワークごとに `CORNUS_CNI_SUBNET_BASE` から `/24`、公開ポートはレプリカ 0 に DNAT、インスタンスごとの `/etc/hosts` 同期、空のときだけコピーするボリューム seeding)。加えて、netns gateway 上のプロセス内 resolver が guest DNS に応答します (`CORNUS_BARE_DNS=false` で無効化)。イメージプルはプレーン HTTP と TLS を自身で選び (`localhost` は自動、`CORNUS_BARE_INSECURE_REGISTRIES` が拡張)、rootfs snapshotter は overlay + native フォールバックです (overlay-backed / docker-in-docker ホストでは `CORNUS_BARE_SNAPSHOTTER=native`)。

`bare` に固有なのは **cornus が supervisor そのもの** である点です。`runc create`/`start` は即座に戻り、runc の `/run` state は tmpfs なので、cornus 自身が pidfd で各 container の PID1 を待ち、restart policy (`no` / `on-failure[:N]` — containerd restart-monitor では表現できません / `always` / `unless-stopped`) を上限付きバックオフで適用して再起動します。二つの supervisor 形態がこのエンジンを共有します。プロセス内 (既定) と、オプトインの **container ごとのデタッチ shim** (`CORNUS_BARE_SHIM`、cornus の conmon 相当) で、後者は cornus の再起動後も存続します。起動時の **reconcile** パスがサーバー再起動後は生存者へ再アタッチし、ホスト再起動後はワークロードを完全に再構築します (netns pin は tmpfs 上にあるため、pin の消失が再起動のシグナルです)。インスタンスごとの状態 — イメージ、snapshot、IP、ポート、restart policy、および期待状態と観測状態 — は `<DataDir>/bare/records/<id>/record.json` として永続化されます。これが containerd のメタデータ DB を置き換えるストアです。

クライアントローカルバインドマウントは既定でほかの host バックエンドと同じ単一ホストの kernel-9p 高速パスを取り、`CORNUS_BARE_REMOTE=1` で caretaker-sidecar パスにオプトインします (`CORNUS_AGENT_IMAGE` が必要)。`dockerhost`/`containerd` と同様、この companion が remote モードで [`cornus port-forward`](/ja/cli/port-forward)/[`cornus tunnel`](/ja/cli/tunnel) を再ルートし、[`cornus exec --forward-agent`](/ja/cli/exec) を有効にします。`containerd` と同等にするため、オプションインターフェース一式 (`MountingBackend`、`EgressBackend`、`RemoteCapable`、ボリューム削除) を実装しています。

**gVisor (`runsc`)。** `CORNUS_BARE_RUNTIME=runsc` を設定すると、各ワークロードが gVisor サンドボックス内で動作します。サンドボックスが guest の cgroup 計測とファイルシステムを所有するため、cornus は 2 つの操作を自動で切り替えます (ランタイム名で検出。`CORNUS_BARE_STATS_SOURCE` で上書き可)。`cornus stats` は host の cgroup ファイルではなくランタイム自身のメトリクス (`runsc events --stats`) を読み、`cornus cp` は host の `/proc/<pid>/root` 経由ではなくコンテナ**内**で `tar` を実行します。ここから 2 点の注意が生じます。`cornus cp` はイメージ内に `tar` バイナリが必要で (scratch/distroless イメージはコピー不可)、コンテナごとのネットワークカウンタは報告されません (`cornus stats` のネットワーク I/O は 0 表示)。それ以外の監督・restart policy・ネットワーク・volume はすべて変わりません。

**`dockerhost` との既知の差:** `containerd` と同様、attach は output-only、ヘルスチェックは無視され警告が出ます。ルートレスは現在対象外で、明確にエラーになります。

## `kubernetes` / `k8s`

`CORNUS_DEPLOY_BACKEND=kubernetes` (または `k8s`) は **client-go** で Kubernetes クラスターにデプロイし、各ワークロードを **デプロイメント** と公開ポート用の **サービス** として描画します。**サーバー / クラスター内専用**で、このバックエンドを指定したローカル `cornus deploy` は警告とともに `dockerhost` へフォールバックします。提供される Kubernetes マニフェストと Helm chart が使用するバックエンドです。

RBAC 範囲かつ名前空間付き (`CORNUS_K8S_NAMESPACE`) です。高度な仕様 block を実現する唯一のバックエンドでもあります。ネットワーク driver pipeline (`CORNUS_K8S_NET_DRIVER`: `services`、Multus 経由の `bridge`/`ipvlan`/`macvlan`、`cilium`) による user ネットワーク、enforcing エグレスプロキシ、Pod ごとの caretaker DNS resolver、資格情報ブローキング、クライアント側エグレス中継、ワークロード間 [hub](/ja/topics/hub) オーバーレイを提供します。ローリング更新はデプロイメントの `strategy.rollingUpdate` に map されます。

CLI を実行する machine ではなく Kubernetes API を通じてデプロイするため、kubernetes バックエンドが[リモートワークフロー](/ja/topics/remote-workflows)を支えます。developer はクラスター内 cornus サーバーを操作し、ポートごとの転送または SOCKS5 conduit がワークロードポートを laptop へ戻します。

## 権限の考え方

**ワークロードを実行するバックエンド** とプロセス内 **ビルドエンジン** は必要な権限が異なります。Cornus サーバーの実行方法はこれで決まります。

- **ビルドを行う** Cornus には昇格が必要です。ビルドエンジンは runc + overlayfs + user 名前空間を実行します。レジストリとデプロイサブシステム単独には不要です。
- `dockerhost` は Docker ソケット、`containerd` はソケット、**root**、CNI プラグイン、`bare` は **root**、OCI ランタイムバイナリ、CNI プラグイン (デーモンソケットは一切不要)、`kubernetes` は RBAC 下のクラスター内実行が必要です。

```sh
# Simplest: run the container privileged (the shipped default).
#   compose: privileged: true   |   k8s: securityContext.privileged: true

# Rootless: run unprivileged with the prerequisites present, then:
cornus serve --rootless          # or CORNUS_ROOTLESS=1
```

ルートレスには `uidmap` (`newuidmap` / `newgidmap`)、`rootlesskit`、`slirp4netns` と適切な `securityContext` が必要です。イメージには `uidmap` が同梱されます。ホストによっては (例: `kernel.apparmor_restrict_unprivileged_userns=1` の最近の Ubuntu) AppArmor プロファイルまたは緩和した sysctl が必要です。

これは **ワークロード** privilege とは別です。サーバーの実行方法にかかわらず default-deny であり、明示的に許可 (`CORNUS_ALLOW_PRIVILEGED`、`CORNUS_ALLOW_BIND_SOURCES`; [認証と TLS](/ja/topics/auth-and-tls)を参照) しない特権コンテナとホストバインドマウントは拒否されます。

## 関連項目

- [`cornus deploy`](/ja/cli/deploy) — 仕様を適用するコマンド。
- [デプロイスペックリファレンス](/ja/reference/deploy-spec) — 全フィールドと各バックエンドの対応。
- [サーバー環境変数](/ja/reference/server-env-vars) — `CORNUS_DEPLOY_BACKEND` とバックエンドごとの設定。
- [リモートワークフロー](/ja/topics/remote-workflows) — laptop から kubernetes バックエンドを操作する方法。
