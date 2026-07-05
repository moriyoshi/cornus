# デプロイスペックリファレンス

**デプロイスペック** は、cornus が実行するワークロードの宣言的説明です。[`cornus deploy -f`](/ja/cli/deploy) に渡す YAML (または JSON) document です。これは *命令的* に適用されます。1 つの仕様が入り、選択された [デプロイバックエンド](/ja/reference/deploy-backends) が actual 状態をそれに収束させます (ワークロードの作成または再作成)。

Compose ファイルや devcontainer は内部で同じ仕様に変換されるため、ここにあるすべてのフィールドは [`cornus compose`](/ja/cli/compose) からも到達できます。4 つのバックエンド、つまり `dockerhost` (既定)、`containerd`、`bare`、`kubernetes` は 1 つのインターフェースの背後にあり、同じ仕様を尊重します。ただし、すべてのフィールドがすべてのバックエンドに map されるわけではありません。ソースがバックエンドごとの挙動を記録している場合は、そのフィールドの説明で明記します。

canonical な正本は [`pkg/.cornus/v1/deploy.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/.cornus/v1/deploy.go) です。

## 例

共通のフィールドといくつかの nested block を示す、比較的完全な仕様です。

```yaml
name: web
image: localhost:5000/web@sha256:1c2d...   # digest-pinned is ideal
replicas: 2
restart: unless-stopped

command: ["--port", "8080"]                 # args to the image ENTRYPOINT
env:
  LOG_LEVEL: info
  DATABASE_URL: postgres://db:5432/app

ports:
  - host: 8080
    container: 80
  - host: 127.0.0.1:5432                     # see hostIP below
    hostIP: 127.0.0.1
    container: 5432

mounts:
  - source: /srv/data
    target: /data
    readOnly: true

volumes:
  - name: web_cache                          # named => shared/persistent
    target: /var/cache
    size: 2Gi

networks:
  - name: myproj_frontend
    aliases: [web, frontend]

resources:
  cpuLimit: 0.5                              # half a core
  memoryLimit: 268435456                     # 256 MiB, in bytes
  reservedMemory: 134217728                  # 128 MiB floor

healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost/healthz"]
  interval: 30s
  timeout: 5s
  retries: 3

labels:
  app.kubernetes.io/part-of: myproj
```

## Top-level fields (`DeploySpec`)

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | デプロイメントを一意に識別します。管理対象 resource は idempotent apply/delete のため、この値で label 付けされます。 |
| `image` | string | yes | — | 実行するイメージ参照。digest-pinned が理想です。 |
| `command` | []string | no | イメージ `CMD` | イメージの既定コマンド (Docker `CMD`) を上書きします。これはイメージ `ENTRYPOINT` への argument であり、ENTRYPOINT は有効なままです。kubernetes ではコンテナの `Args` に入るため、イメージ entrypoint は保持されます。 |
| `entrypoint` | []string | no | イメージ `ENTRYPOINT` | イメージ entrypoint (Docker `ENTRYPOINT` / Kubernetes コンテナ `command`) を上書きします。設定時、`command` がその argument を与えます。空の場合はイメージ既定を保ちます。 |
| `env` | map[string]string | no | — | 環境変数。map から `KEY=VALUE` として適用されます。 |
| `ports` | [][PortMapping](#portmapping) | no | — | ホストポートをコンテナポートに map します。 |
| `mounts` | [][Mount](#mount) | no | — | ホストパスをコンテナにバインドします。 |
| `volumes` | [][VolumeSpec](#volumespec) | no | — | バックエンドがストレージを用意する管理対象 (non-bind) ボリューム。 |
| `networks` | [][NetworkAttachment](#networkattachment) | no | — | このワークロードが参加する user-defined ネットワーク (Compose `networks:`)。空のは既定 connectivity のみを意味します。 |
| `proxy` | [ProxySpec](#proxyspec) | no | — | userspace enforcing エグレスプロキシを要求します。**kubernetes のみ** (dockerhost は libnetwork で isolation を得るため無視します)。 |
| `dns` | [DNSSpec](#dnsspec) | no | — | pod ごとの caretaker DNS resolver を要求します。**kubernetes のみ。** |
| `hub` | [HubSpec](#hubspec) | no | — | ワークロードをサーバーのワークロード間オーバーレイに参加させます。**kubernetes のみ。** [ワークロード間 hub](/ja/topics/hub) を参照してください。 |
| `docker` | [DockerSpec](#dockerspec) | no | — | ワークロードに Docker エンジン API エンドポイントを公開します。**kubernetes のみ。** サーバーに `CORNUS_CLIENT_TOKEN_SECRET` が必要です。 |
| `credentials` | [CredentialSpec](#credentialspec) | no | — | 短命なクライアントが発行した資格情報をワークロードへ broker します。**kubernetes** で実現されます。ホストバックエンドでは companion caretaker 経由です。他バックエンドは警告して無視します。[資格情報ブローキング](/ja/topics/credentials) を参照してください。 |
| `restart` | string | no | `unless-stopped` | 再起動ポリシー: `no`、`always`、`on-failure`、`unless-stopped`。 |
| `restartMaxAttempts` | int | no | `0` (バックエンド既定, unlimited) | `on-failure` ポリシーの再起動 attempt を cap します。**dockerhost のみ** (kubernetes と containerd は count を bound できないため無視)。 |
| `replicas` | int | no | バックエンド既定 | desired インスタンス count。すべてのバックエンドで尊重されます。ホストバックエンドでは公開済みホストポートはレプリカ 0 にだけ向きます。 |
| `privileged` | bool | no | `false` | 完全な privilege で実行します (Docker `--privileged` / Kubernetes `securityContext.privileged`)。opt-in です。default-deny posture は [Auth and TLS](/ja/topics/auth-and-tls) を参照してください。 |
| `healthcheck` | [Healthcheck](#healthcheck) | no | — | コンテナ health probe。 |
| `resources` | [Resources](#resources) | no | — | CPU/memory limit と reservation。 |
| `updateConfig` | [UpdateConfig](#updateconfig) | no | — | Rolling-update strategy。**kubernetes のみ** (ホストバックエンドは単一インスタンスを recreate して無視します)。 |
| `user` | string | no | イメージ既定 | プロセスを実行する user (および任意の group): `uid`、`uid:gid`、`user`、`user:group`。kubernetes は **numeric** `uid[:gid]` のみを map でき、username は表現できません。 |
| `workingDir` | string | no | イメージ既定 | コンテナ working ディレクトリ (compose `working_dir`)。 |
| `hostname` | string | no | バックエンド既定 | コンテナ hostname (compose `hostname`)。 |
| `labels` | map[string]string | no | — | user metadata。kubernetes では pod-template **annotation** になります (label ではありません)。キー clash では cornus 自身の management label が常に勝ちます。 |
| `stopSignal` | string | no | イメージ既定 | main プロセスを停止する signal。例: `SIGTERM`。dockerhost のみ。kubernetes と containerd は無視します。 |
| `stopGracePeriod` | string | no | バックエンド既定 | stop signal 後、kill まで待つ時間。Go duration (`10s`、`1m30s`)。containerd は無視します。 |
| `init` | bool (nullable) | no | バックエンド既定 | `true` は zombie を reap する PID-1 init を要求し、`false` は拒否します (compose `init`)。dockerhost のみ。kubernetes と containerd は無視します。 |
| `tty` | bool | no | `false` | pseudo-TTY を割り当てます (compose `tty`)。 |
| `stdinOpen` | bool | no | `false` | コンテナの stdin を開いたままにします (compose `stdin_open`)。containerd は無視します。 |
| `readOnly` | bool | no | `false` | root ファイルシステムを読み取り専用でマウントします (compose `read_only`)。 |
| `capAdd` | []string | no | — | Linux capability を追加します (compose `cap_add`)。 |
| `capDrop` | []string | no | — | Linux capability を drop します (compose `cap_drop`)。 |
| `securityOpt` | []string | no | — | セキュリティオプション (compose `security_opt`)。dockerhost はそのまま渡します。kubernetes/containerd は well-known なもの (`no-new-privileges`、`label=`) だけを map し、`seccomp=` / `apparmor=` では警告します。 |
| `groupAdd` | []string | no | — | supplementary group (compose `group_add`)。kubernetes/containerd は **numeric GID のみ**を受け付け、name は警告してスキップします。 |
| `sysctls` | map[string]string | no | — | namespaced kernel パラメーター (compose `sysctls`)。 |
| `extraHosts` | []string | no | — | `host:ip` 形式のカスタム `/etc/hosts` entry (compose `extra_hosts`)。containerd は無視します。 |
| `dnsServers` | []string | no | — | カスタム nameserver (compose `dns`)。caretaker フィールドの `dns` とは別です。containerd は無視します。 |
| `dnsSearch` | []string | no | — | カスタム DNS search ドメイン (compose `dns_search`)。containerd は無視します。 |
| `dnsOptions` | []string | no | — | カスタム resolver オプション (compose `dns_opt`)。各 item は `name` または `name:value`。containerd は無視します。 |
| `ulimits` | [][Ulimit](#ulimit) | no | — | resource ごとの rlimit (compose `ulimits`)。kubernetes は無視します。 |
| `tmpfs` | []string | no | — | tmpfs マウント。各 item はコンテナパスと任意の `:` 区切りオプション (例: `/run:size=64m`)。 |
| `devices` | []string | no | — | ホスト device mapping (compose `devices`)。各 item は `host:container[:perms]` (perms 既定は `rwm`)。kubernetes は無視します。 |
| `shmSize` | int64 | no | `0` (バックエンド既定) | `/dev/shm` size。byte 単位 (compose `shm_size`)。 |
| `pidMode` | string | no | バックエンド既定 | PID 名前空間モード (compose `pid`)。例: `host`。kubernetes/containerd は `host` だけを map します。 |
| `ipcMode` | string | no | バックエンド既定 | IPC 名前空間モード (compose `ipc`)。例: `host`。kubernetes/containerd は `host` だけを map します。 |
| `egress` | [EgressSpec](#egressspec) | no | — | outbound トラフィックをクライアント側 vantage point 経由にします。[クライアント側エグレス](/ja/topics/egress) を参照してください。 |
| `ingress` | [IngressSpec](#ingressspec) | no | — | ワークロードの公開済みポートの前にパブリック HTTP(S) イングレスを要求します。**kubernetes** のみ (ホストバックエンドは警告して無視)。[イングレス](/ja/topics/ingress) を参照してください。 |

::: tip
`restart` は Compose の `deploy.restart_policy.condition` (`none`→`no`、`on-failure`→`on-failure`、`any`→`always`) から map されます。planner が仕様を書くとき、これは service-level の `restart:` より authoritative です。
:::

## Nested types

### PortMapping

ホストポートをコンテナポート (`ports[]`) に map します。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `host` | int | yes | — | publish するホストポート。 |
| `container` | int | yes | — | 到達するコンテナポート。 |
| `protocol` | string | no | `tcp` | `tcp` または `udp`。 |
| `hostIP` | string | no | `0.0.0.0` (all interfaces) | ホスト側 publish を特定インターフェースに制限します (compose `127.0.0.1:8080:80`)。ホストバックエンドでは尊重されます。kubernetes サービスには相当するものがありません。 |

### マウント

ホストソースをコンテナにバインドします (`mounts[]`)。管理対象 [`volumes`](#volumespec) entry とは別物です。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `source` | string | yes | — | バインドするホストパス。 |
| `target` | string | yes | — | マウント先コンテナパス。 |
| `readOnly` | bool | no | `false` | 読み取り専用でマウントします。 |
| `selinux` | string | no | — | SELinux relabel (compose `:z`/`:Z`): `z` は content をコンテナ間で共有し、`Z` はプライベートにします。dockerhost で適用されます。containerd/kubernetes は relabel しません。 |

### VolumeSpec
| `immutable` | bool | no | `false` | デプロイメントの存続中に内容が変わらないクライアントローカルの読み取り専用マウント。サーバーのファイル単位キャッシュを有効にします。サーバーホストのマウントでは無視されます。 |
| `asyncCache` | bool | no | `false` | キャッシュ整合性を保つ block protocol を使うクライアントローカルの書き込み可能マウント。replica は 1 つ必要で、`readOnly` または `immutable` とは併用できません。サーバーホストのマウントでは無視されます。 |

コンテナにマウントされる管理対象 (non-bind) ボリュームです (`volumes[]`)。kubernetes では dynamically-provisioned PersistentVolumeClaim になります。dockerhost では Docker anonymous/named ボリュームです。初回 start 時、ボリュームはイメージが `target` に持っている内容で初期設定されます (Docker ボリューム semantics)。以後の start では書き込みが保持されます。

`name` フィールドは 2 つの Compose ボリューム flavor を選びます。

- **匿名** (`name` 空の): ストレージはこのデプロイメントにプライベートかつ一時的です。デプロイメント delete 時に reap されます (`docker rm -v` と同様)。
- **名前付き** (`name` set): 共有で project-scoped なストアです。ライフサイクルは単一デプロイメントから独立しています。そのボリュームを使う単一デプロイメントの `cornus delete` 後も **survive** します。すでに project-scoped な logical name (例: `myproj_cache`) を渡してください。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | no | 匿名 | set => shared/persistent 名前付きボリューム。空の => 匿名。 |
| `target` | string | yes | — | コンテナマウントパス。 |
| `size` | string | no | `1Gi` | 要求する size。例: `1Gi`。 |
| `storageClass` | string | no | クラスター既定 class | PVC 用 Kubernetes StorageClass。 |
| `readOnly` | bool | no | `false` | 読み取り専用でマウントします。 |
| `driver` | string | no | Docker 既定 (`local`) | **名前付き** ボリューム用のボリュームプラグイン (compose `driver`)。dockerhost のみ。kubernetes/containerd は無視します。 |
| `driverOpts` | map[string]string | no | — | opaque driver オプション (compose `driver_opts`)。dockerhost のみ。 |
| `labels` | map[string]string | no | — | **名前付き** ボリューム上の user metadata。dockerhost は設定し、kubernetes は PVC にコピーします (management label が勝ちます)。containerd は無視します。 |

### NetworkAttachment

ワークロードの user-defined ネットワーク (`networks[]`) への membership 1 つです。Docker/Compose user-network semantics に沿っています。member は同じネットワークの他 member からサービス name (および alias) で到達でき、fabric が対応する場合は、参加していないネットワークから隔離されます。

`driver` は kubernetes バックエンドがネットワークをどう実現するかを選びます。空の場合はバックエンド既定 (`CORNUS_K8S_NET_DRIVER`、それ自体の既定は `services`) です。認識される kubernetes driver: `services` (DNS のみ、任意のクラスター)、`bridge`/`ipvlan`/`macvlan` (Multus CNI)、`cilium`。dockerhost バックエンドは `driver` を Docker 自身のネットワーク driver にそのまま渡します。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | project-scoped ネットワーク resource name (例: `myproj_frontend`)。 |
| `driver` | string | no | `services` (kubernetes) / Docker bridge | realisation driver (上記参照)。 |
| `driverOpts` | map[string]string | no | — | driver に転送される opaque per-network knob (compose `driver_opts`)。 |
| `aliases` | []string | no | — | この member のネットワーク上での追加 DNS name。 |
| `default` | bool | no | `false` | kubernetes の detached-primary モード: pod の主インターフェースを置き換えます (Multus default-network)。設定できる attachment は最大 1 つです。dockerhost は無視します。 |
| `ip` | string | no | — | このネットワーク上の member IPv4 アドレスを CIDR form で pin します (例: `10.222.14.7/24`)。Multus-realised ネットワークのみ。dockerhost は無視します (libnetwork はネイティブにアドレスを扱います)。 |
| `subnet` | string | no | — | ネットワーク IPAM subnet (compose `ipam.config[0].subnet`)。dockerhost と Multus netdriver が使います。containerd は無視します。 |
| `gateway` | string | no | — | ネットワーク IPAM gateway。dockerhost のみ。 |
| `ipRange` | string | no | — | ネットワーク IPAM IP range。dockerhost のみ。 |
| `internal` | bool | no | `false` | 外部エグレスなしの intra-network トラフィックに制限します (compose `internal`)。dockerhost のみ。 |
| `attachable` | bool | no | `false` | 単独コンテナが swarm-scoped ネットワークに join できるようにします (compose `attachable`)。dockerhost のみ。 |
| `enableIPv6` | bool | no | `false` | IPv6 addressing を有効化します (compose `enable_ipv6`)。dockerhost のみ。 |
| `labels` | map[string]string | no | — | ネットワーク上の user metadata。dockerhost のみ (management label が勝ちます)。 |
| `ipv6` | string | no | — | この member の per-network IPv6 アドレスを pin します (compose `ipv6_address`)。dockerhost のみ。 |
| `mac` | string | no | — | この member の MAC アドレスを pin します (compose `mac_address`)。dockerhost のみ。 |
| `priority` | int | no | `0` | ネットワーク attachment の順序 (compose `priority`)。最も priority が高いネットワークが先に join され、その gateway が既定経路になります。dockerhost のみ。 |

### ProxySpec

ワークロード用の userspace エグレスプロキシを設定します (`proxy`)。**kubernetes のみ。** `allow` はワークロードが到達可能な peer サービス name の集合です (プロキシネットワークを共有するサービス)。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `mode` | string | no | `enforcing` | `enforcing` (すべての outbound TCP を nftables sidecar に redirect し、`allow` peer に解決される宛先だけを許可します。実 L4 isolation) または `cooperative` (soft isolation: 各 `allow` peer の DNS name は sidecar が転送する loopback アドレスを指します。生の pod IP へ接続すれば bypass 可能)。 |
| `allow` | []string | no | — | ワークロードが到達できる peer サービス name。 |
| `ports` | map[string][]int | no | — | Cooperative モード: `allow` peer ごとのプロキシ対象コンテナポート。 |
| `listenPort` | int | no | バックエンド既定 | redirected トラフィック用に sidecar が listen するポート。 |

### DNSSpec

pod ごとの caretaker DNS resolver を設定します (`dns`)。**kubernetes のみ。** `records` は peer サービス name を pod が解決すべき IPv4 アドレスに map します (通常は peer の user-network / Multus-secondary アドレス)。`records` にないものはすべてクラスター DNS へ転送されます。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `records` | map[string]string | no | — | Peer サービス name → 解決先 IPv4 アドレス。 |
| `requireUserNet` | bool | no | `false` | record が Multus 副アドレスを指すことを示します。クラスターが Multus fabric を実現できない場合、バックエンドは DNS caretaker 全体をスキップし、resolution はクラスター DNS に degrade します。 |

### DockerSpec

caretaker の Docker エンジン API エンドポイントを設定します (`docker`)。**kubernetes のみ。** caretaker は pod-loopback エンドポイント上に Docker-API プロキシをバインドし、`DOCKER_HOST` を注入します。これにより標準 `docker` / `docker compose` が、pod 自身の stack を管理している同じ cornus サーバーを操作できます。サーバーに client-scoped トークンシークレット (`CORNUS_CLIENT_TOKEN_SECRET`) が必要です。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `transport` | string | no | `tcp` | `tcp` (`127.0.0.1:port` にバインド)、`unix` (`socketPath` にソケットをバインド)、または `both` (`DOCKER_HOST` は TCP エンドポイントを指します)。 |
| `port` | int | no | `2375` | `tcp` / `both` 転送経路用 loopback TCP ポート。 |
| `socketPath` | string | no | `/cornus/docker/docker.sock` | `unix` / `both` 転送経路用 Unix ソケットパス (共有 emptyDir 上)。 |
| `envVar` | string | no | `DOCKER_HOST` | エンドポイントを app コンテナに知らせる環境変数。 |

### HubSpec
### TelemetrySpec

caretaker 内で組み込み OpenTelemetry Collector を実行します (Compose の service または project level の `x-cornus-telemetry:`、CLI の `--telemetry-*`)。アプリは pod-loopback receiver へ OTLP を送り、Collector は `endpoint` に export します。バックエンドはワークロードの `OTEL_*` env を自動注入します。全バックエンド対応です。[Observability](/ja/guides/observability#workload-telemetry)を参照してください。collector 有効イメージが必要です (`-tags otelcol`、リリースイメージでは設定済み)。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `enabled` | bool | no | `false` | telemetry を有効にします。非空の `endpoint` も有効化します。 |
| `endpoint` | string | yes | — | export 先の外部 OTLP backend (`grpc` では `host:port`、http/protobuf では URL)。 |
| `protocol` | string | no | `grpc` | exporter protocol: `grpc` または `http/protobuf`。 |
| `headers` | map[string]string | no | — | 静的 export header。Kubernetes では Pod スペックではなく Deployment 所有 Secret で投影されます。 |
| `insecure` | bool | no | `false` | backend への転送セキュリティを無効にします。 |
| `signals` | []string | no | すべて | pipeline を `traces`、`metrics`、`logs` に制限します。 |
| `serviceName` | string | no | デプロイメント名 | 注入される `OTEL_SERVICE_NAME` を上書きします。 |
| `resourceAttributes` | map[string]string | no | — | cornus 由来の既定値と統合する追加 `OTEL_RESOURCE_ATTRIBUTES`。 |
| `grpcPort` / `httpPort` | int | no | `4317` / `4318` | pod 内 OTLP receiver loopback ポート。 |
| `debug` | bool | no | `false` | 収集した telemetry も collector stdout に出力します。 |


ワークロード間オーバーレイ membership を要求します (`hub`)。**kubernetes のみ。** [ワークロード間 hub](/ja/topics/hub) を参照してください。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `identity` | string | no | デプロイメント name | ポリシー ID。 |
| `export` | [][HubExport](#hubexport-hubimport-hubimportdynamic) | no | — | このワークロードがオーバーレイ上でホストするサービス。 |
| `import` | [][HubImport](#hubexport-hubimport-hubimportdynamic) | no | — | このワークロードがオーバーレイ経由で到達するサービス。 |
| `importDynamic` | [HubImportDynamic](#hubexport-hubimport-hubimportdynamic) | no | — | ワークロードを動的インポート discovery に opt in します。 |

#### HubExport / HubImport / HubImportDynamic

**`HubExport`** — このワークロードがオーバーレイ上でホストするサービス 1 つ:

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | オーバーレイ上のサービス name。 |
| `port` | int | yes | — | サービスが listen するポート。 |
| `deliver` | bool | no | `false` | イングレス配送を要求します (hub がこの pod へ中継し、pod が localhost の `port` に接続)。これによりサービスは hub から到達可能でなくても構いません。 |
| `protocol` | string | no | `tcp` | `tcp` または `udp`。 |

**`HubImport`** — このワークロードがオーバーレイ経由で到達するサービス 1 つ:

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | 到達するサービス name。 |
| `ports` | []int | yes | — | loopback リスナーをバインドするポート。 |
| `protocol` | string | no | `tcp` | `tcp` または `udp`。 |

**`HubImportDynamic`** — hub catalog プッシュを subscribe し、catalog に載る **すべての** サービス (このワークロード自身のエクスポートと静的インポートを除く) の synthetic IP に loopback リスナーをバインドします。サービスの出現/消滅に応じてリスナーを追加/close します。デプロイ時に name が不明なため、DNS record は配線されません。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `ports` | []int | yes | — | discovered サービスごとにバインドされる共有ポート set。 |
| `protocol` | string | no | `tcp` | `tcp` または `udp`。 |

### CredentialSpec

client-sourced 資格情報をワークロードに仲介します (`credentials`)。シークレット値はクライアント上で発行され (この仕様には決して含まれません)、cornus サーバーと caretaker sidecar 経由で配送されます。フォアグラウンドの `cornus deploy --server` セッション上の **kubernetes** で実現されます。ホストバックエンドでは companion caretaker 経由です。`--detach` とその他バックエンドは拒否 / ignore します。[資格情報ブローキング](/ja/topics/credentials) を参照してください。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `sources` | [][CredentialSource](#credentialsource) | no | — | 各 entry はコンテナが必要に応じて retrieve できる資格情報 1 つです。 |

#### CredentialSource

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | logical 資格情報 name。capability キーと既定ファイル basename / エンドポイントパス segment を兼ねます。 |
| `backend` | string | yes | — | 資格情報を発行するクライアント側バックエンド (例: `aws-sts`、`static`、`exec`)。呼び出し元の machine 上で、呼び出し元自身の cloud/API 資格情報で実行されます。 |
| `config` | map[string]string | no | — | non-secret バックエンド configuration (例: `role_arn`、`duration`、`region`)。シークレット自体を絶対に保持してはいけません。 |
| `ttl` | string | no | バックエンド既定 | クライアント側 cache/refresh hint。Go duration 文字列。 |
| `deliver` | [][CredentialDelivery](#credentialdelivery) | no | — | コンテナが資格情報を消費する方法。空のでも有効です (取得可能だが対象範囲されない)。 |

#### CredentialDelivery

資格情報をコンテナから利用できるようにする provider-agnostic な方法 1 つです。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `kind` | string | no | `endpoint` | `endpoint` (HTTP metadata サーバー / auth-injecting プロキシ)、`file` (共有ボリューム内のパスに実体化)、または `env` (app コンテナ環境に注入)。 |
| `provider` | string | no | `generic` | **エンドポイント kind。** `generic` は cornus-native JSON contract (`GET /credentials/<name>`) を提供します。`aws-imds` と将来の adapter は、同じ資格情報を cloud SDK が期待する形で描画します。 |
| `wellKnown` | bool | no | `false` | **エンドポイント kind。** pod netns 内で provider の canonical link-local アドレス (例: AWS `169.254.169.254`、IMDSv2) をバインドします。`NET_ADMIN` が必要です。false の場合、エンドポイントは loopback にバインドされ、injected env var で通知されます (`aws-imds` では `AWS_CONTAINER_CREDENTIALS_FULL_URI`、ECS container-credentials エンドポイント)。 |
| `upstream` | string | no | provider 既定 | **エンドポイント kind、auth-proxy provider。** プロキシが転送する vendor API を上書きします (例: Anthropic-/OpenAI-compatible gateway)。Non-secret。 |
| `path` | string | no | — | **ファイル kind。** 資格情報を実体化するコンテナパス。 |
| `format` | string | no | `json` | **ファイル kind。** `json` (neutral な `{values,expiration}` object)、`env` (`KEY=VALUE` lines)、`raw` (単一値)、または `aws-credentials` (ini プロファイル)。 |
| `envVar` | string | no | — | **env kind。** 設定する app-container 環境変数。デプロイ time に Kubernetes シークレット (`secretKeyRef`) へ一度取得されます。静的でランタイム refresh はなく、etcd に残ります。短命資格情報には proxy/file 配送を推奨します。 |
| `valueKey` | string | no | `value` then `token` | **env kind。** どの資格情報 values キーが env 値を供給するか。 |

### ヘルスチェック

コンテナ health probe (`healthcheck`) です。Docker のヘルスチェックを model にしています。dockerhost では Docker コンテナヘルスチェックになり、kubernetes では exec liveness (および readiness) probe になります。`test` は Docker の `CMD` form を使います。最初の element は `CMD` (残りを exec)、`CMD-SHELL` (単一文字列を shell で実行)、または `NONE` (継承されたヘルスチェックを無効化) です。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `test` | []string | no | — | Docker `CMD` form の probe コマンド (上記参照)。 |
| `interval` | string | no | バックエンド既定 | Probe interval。Go duration 文字列 (`30s`)。 |
| `timeout` | string | no | バックエンド既定 | probe ごとのタイムアウト。Go duration 文字列。 |
| `startPeriod` | string | no | バックエンド既定 | failure を count し始める前の grace period。Go duration 文字列。 |
| `startInterval` | string | no | バックエンド既定 | start period **中**の probe interval (compose `start_interval`)。 |
| `retries` | int | no | バックエンド既定 | unhealthy とみなすまでの consecutive failure 数。 |

::: 警告 containerd
containerd バックエンドはヘルスチェックを無視します (警告付き)。
:::

### Resources

ワークロードの compute を cap する (`*Limit` フィールド)、または guaranteed floor を reserve します (`reserved*` フィールド、compose `deploy.resources.reservations` 由来)。zero フィールドは「その axis は unset」を意味します。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `cpuLimit` | float64 | no | `0` (unset) | fractional core count (例: `0.5` = half a core)。Docker `NanoCpus`、kubernetes CPU quantity in millicores。 |
| `memoryLimit` | int64 | no | `0` (unset) | byte count。Docker `Memory`、kubernetes memory quantity。 |
| `reservedCpu` | float64 | no | `0` (unset) | Reservation floor。kubernetes `resources.requests.cpu`。**dockerhost では no-op** (Docker に CPU reservation はありません)。containerd は無視します。 |
| `reservedMemory` | int64 | no | `0` (unset) | Reservation floor。kubernetes `resources.requests.memory`。dockerhost `MemoryReservation`。containerd は無視します。 |

### UpdateConfig

rolling-update strategy (`updateConfig`、compose `deploy.update_config` 由来) です。**kubernetes だけが** デプロイメント `strategy.rollingUpdate` に map します。他の compose knob (`delay`、`monitor`、`max_failure_ratio`) は swarm concept でありデプロイメントでは表現できないため、translate 時に drop されます。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `parallelism` | int | no | `0` (バックエンド既定 of 1) | 一度に update するインスタンス数。`maxUnavailable` (stop-first) または `maxSurge` (start-first) の size になります。 |
| `order` | string | no | `stop-first` | `stop-first` (新しいインスタンスを起動する前に古いインスタンスを落とす) または `start-first` (古いものを削除する前に新しいインスタンスを surge する)。 |

### Ulimit

プロセス resource limit 1 つです (`ulimits[]`、compose `ulimits`)。Compose の shorthand (裸の integer) は `soft == hard` を設定します。dockerhost `HostConfig.Ulimits`、containerd OCI `Process.Rlimits`。kubernetes は無視します。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | 素の limit name (`nofile`、`nproc`)。 |
| `soft` | int64 | no | — | soft bound。 |
| `hard` | int64 | no | — | hard bound。 |

### EgressSpec

ワークロードの **outbound** トラフィックをクライアント側 vantage point 経由に経路します (`egress`)。air-gapped クラスターや、認可されたエグレスパスが呼び出し元側にある VPN/corporate-proxy/SASE ネットワーク向けです。[クライアント側エグレス](/ja/topics/egress) を参照してください。

ルーティングは宛先ごとです。各 flow は 4 つの経路のいずれかに送られます。`client` (クライアント側ネットワークへ中継)、`gateway` (永続的 egress-gateway ノードへ中継、`--detach` 用)、`cluster` (中継なしで直接エグレス)、`deny` (drop) です。`default` は unmatched 宛先に適用され、既定は `cluster` です。そのためエグレスを有効化してもクラスター内トラフィックが黙って逸らされることはありません。宛先を client/gateway へ **out** させることを明示的に選びます。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `mode` | string | no | `env` | `env` (`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`/`ALL_PROXY` をコンテナに伝搬。すべてのバックエンド、中継なし)、`proxy` (caretaker が HTTP CONNECT + SOCKS5 転送プロキシを実行し、サーバー経由でクライアントへ中継。現在は kubernetes、ホストバックエンドは companion caretaker 経由)、または `transparent` (すべての outbound TCP を nftables redirect で捕捉して中継。現在は kubernetes)。 |
| `gateway` | string | no | — | **Reserved; today は空のでなければなりません。** `gateway` 経路は現在 cornus サーバー自身を通じてエグレスします。non-empty 値は validation で拒否されます。 |
| `proxies` | map[string]string | no | client-resolved | モード `env`: 注入する明示的なプロキシ変数。空の場合、クライアントがデプロイ time に自分の OS プロキシ configuration を解決します。 |
| `rules` | [][EgressRule](#egressrule) | no | — | 宣言的ルーティングポリシー。ordered list で first-match-wins、フォールバックは `default`。`script` に supersede されます。 |
| `script` | string | no | — | 経路を宛先ごとに決める任意の PAC-style JavaScript (`FindProxyForURL`)。設定されている場合は `rules` を supersede します。`DIRECT`→`cluster`、`PROXY client`/`PROXY gateway`→中継経路、`DENY`→drop、match なし→`default`。 |
| `default` | string | no | `cluster` | rule/script に match しない宛先の経路: `cluster`、`client`、`gateway`、または `deny`。 |
| `listenPort` | int | no | バックエンド既定 | caretaker プロキシの listen ポート (モード `proxy` と `transparent`)。 |

モード `proxy` と `transparent` はトラフィックをクライアント経由でトンネルするため稼働中 deploy-attach セッションが必要です (stateless `--detach` デプロイでは使えません)。`env` は必要ありません。

#### EgressRule

宛先を経路に map します (`egress.rules[]`)。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `pattern` | string | yes | — | 宛先ホスト (glob、例: `*.internal`)、CIDR (例: `10.0.0.0/8`)、および/または明示的なポート (例: `api.example.com:443`、`10.0.0.0/8:5432`) に match します。ホストまたはポート part が空の場合は任意に match します。 |
| `route` | string | yes | — | `client`、`gateway`、`cluster`、`deny` のいずれか。 |

### IngressSpec

ワークロードの `ClusterIP` サービスの前にパブリック HTTP(S) イングレスを要求します (`ingress`)。**Kubernetes-backend のみ** です。仕様は少なくとも 1 つのポートを publish する必要があります (そのサービスがイングレスバックエンドになります)。`dockerhost` / `containerd` は警告して無視します。[イングレス](/ja/topics/ingress) を参照してください。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `enabled` | bool | no | `false` | イングレスを有効化します。non-empty な `hosts` (または Compose `host:`) は `enabled` を imply します。裸の `x-cornus-ingress: {}` はすべてのフィールドを既定にして有効化します。 |
| `hosts` | []string | no | derived | 外部 hostname。各 hostname は、1 つの TLS entry を共有する個別のイングレス規則になります。`@` は apex (base ドメイン自体、`<name>.` prefix なし) に map されます。空の場合は単一の `<subdomain>.<domain>` ホストを導出します。ホストも base ドメインもなければ拒否されます。 |
| `domain` | string | no | `CORNUS_INGRESS_DOMAIN` | `hosts` が空の場合にホストを auto-derive する base ドメインのクライアント上書き。サーバーは resolved ホストが自分のドメイン内に留まることを強制できます (`CORNUS_INGRESS_ENFORCE_DOMAIN`)。 |
| `subdomain` | string | no | デプロイメント name | auto-derive 時に base ドメインの前に付く label (`<subdomain>.<domain>`)。Compose translator は `<service>.<project>` を設定します。DNS-1123 に sanitize されます。 |
| `path` | string | no | `/` | 経路する HTTP パス prefix。 |
| `pathType` | string | no | `Prefix` | Kubernetes パス match 型: `Prefix`、`Exact`、または `ImplementationSpecific`。 |
| `port` | int | no | first 公開済み | イングレスが経路するコンテナポート。non-zero の場合は仕様の公開済みポートのいずれかと一致する必要があります。 |
| `className` | string | no | `CORNUS_INGRESS_CLASS`, then クラスター既定 | イングレスの `IngressClassName`。 |
| `annotations` | map[string]string | no | — | controller-specific knob 用にイングレス object へそのまま統合されます。 |
| `tls` | [IngressTLS](#ingresstls) | no | — | 設定するとホスト(s) に HTTPS を要求します。プレーン HTTP では省略します。 |

#### IngressTLS

イングレスホスト(s) 用 HTTPS を設定します (`ingress.tls`)。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `secretName` | string | no | `<name>-tls` | 提供する既存 TLS シークレット。`clusterIssuer` (またはサーバー既定) が設定されている場合、既定は cert-manager により用意されます。 |
| `clusterIssuer` | string | no | `CORNUS_INGRESS_TLS_ISSUER` | cert-manager が証明書を用意するよう、`cert-manager.io/cluster-issuer` annotation を設定します。 |

### KnativeSpec

ワークロードを Knative Serving Service (`knative`) としてデプロイします。`serving.knative.dev` を提供する Kubernetes バックエンドだけが実現し、その場合 backend は Deployment と Service の代わりに `serving.knative.dev/v1` Service を作成し、Knative が autoscaling、scale-to-zero、Route を管理します。通常クラスターと `dockerhost` / `containerd` / `bare` では警告して無視します。通常は `cornus deploy -f service.yaml` の Knative descriptor loader が設定します。詳細は [`cornus deploy`](/ja/cli/deploy) を参照してください。

| フィールド | 型 | 必須 | 既定 | 説明 |
| --- | --- | --- | --- | --- |
| `enabled` | bool | no | `false` | Knative Service としてマークします。bare `{}` はすべての既定値で有効にします。 |
| `minScale` / `maxScale` | int | no | `0` | autoscaling floor / ceiling。`0` は scale-to-zero / unlimited を意味します。 |
| `target` | int | no | — | replica ごとの autoscaling target。 |
| `concurrency` | int | no | `0` | replica あたりの同時 request の上限。`0` は unlimited。 |
| `class` | string | no | cluster default | autoscaler class: `kpa` または `hpa`。 |
| `metric` | string | no | `concurrency` | scaling metric: `concurrency`、`rps`、`cpu` (`cpu` は `class: hpa` が必要)。 |
| `timeoutSeconds` | int | no | `300` | 1 request の最大時間。 |
| `port` | int | no | first published | Knative が経路する単一コンテナポート。 |
| `annotations` | map[string]string | no | — | 上記以外の autoscaling knob 用に revision template へ統合します。 |

## 関連ページ

- [`cornus deploy`](/ja/cli/deploy) - 仕様を適用するコマンド。
- [デプロイ backends](/ja/reference/deploy-backends) - `dockerhost`、`containerd`、`bare`、`kubernetes` がこれらのフィールドをどう実現するか。
- [クライアント側エグレス](/ja/topics/egress) - `egress` block の詳細。
- [イングレス](/ja/topics/ingress) - `ingress` block の詳細。
- [資格情報ブローキング](/ja/topics/credentials) - `credentials` block の詳細。
- [ワークロード間 hub](/ja/topics/hub) - `hub` block とワークロード間オーバーレイ。
