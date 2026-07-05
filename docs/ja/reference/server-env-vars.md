# サーバー環境変数

このページは、[`cornus serve`](/ja/cli/serve) とサーバーサブシステムが読む `CORNUS_*` 環境変数を一覧します。一部は `cornus serve` のフラグに対応します (下に記載)。多くは環境変数だけで設定する項目であり、サーバー、デプロイバックエンド、ビルドエンジン、トンネルから直接読み取られます。

::: info
この list はソース tree (`grep 'CORNUS_[A-Z0-9_]+' pkg cmd`) から導いた実用リファレンスです。内部または変化中の knob が少し含まれる場合があります。authoritative な挙動は常に code にあります。test-only 変数 (`CORNUS_TEST_*`) は省略しています。CLI が消費するクライアント側変数 (サーバーではない) は最後に別 group としてまとめています。
:::

## General / リスナー

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_ADDR` | `--addr` | `:5000` | `/v2/*` と `/.cornus/v1/*` の HTTP listen アドレス。 |
| `CORNUS_DATA` | — | platform データディレクトリ | サーバーデータディレクトリ (レジストリファイルシステムストア、upload、バックエンド状態)。 |
| `CORNUS_ROOTLESS` | `--rootless` | off | ビルドエンジンをルートレスモード (user 名前空間) で実行します。 |
| `CORNUS_LOG_LEVEL` | — | `info` | ログ verbosity (`debug`、`info`、`warn`、`error`)。 |
| `CORNUS_ADVERTISE_URL` | — | — | Pod のマウントエージェントや caretaker が接続し直すクラスター内 Cornus URL。Kubernetes バックエンドでクライアントローカルマウントに必要です。 |
| `CORNUS_ADVERTISE_REGISTRY` | — | derived | デプロイ対象がプルできるレジストリとしてサーバーがクライアントへ通知する `host[:port]` (および任意の scheme) を上書きします (`GET /.cornus/v1/info`)。 |
| `CORNUS_REPLICA_ID` | — | — | このレプリカの固定 ID。分散型ハブストアと GC のリーダー選出による制御で使われます。 |

## ストレージ

バックエンド catalog 全体は [ストレージ backends](/ja/reference/storage-backends) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | データディレクトリ下のファイルシステム | レジストリ永続化バックエンド: パス、`file://`、`mem://`、`s3://bucket?region=&endpoint=&path_style=`、または (`-tags cloudblob` の背後で) `gs://` / `azblob://`。 |

## リモート 9P ファイルキャッシュと書き込み可能マウント

これらの設定は、不変のクライアントローカルマウントに使うキャッシュと、書き込み可能な `,async` マウントの任意の整合性機能を制御します。ファイルキャッシュはサーバー専用です。endpoint は共有する機能セットを交渉するため、整合性フラグはサーバー環境と deploy caller 環境の両方で設定する必要があります。

| 変数 | フラグ | 既定 | 意味 |
| --- | --- | --- | --- |
| `CORNUS_FILE_CACHE` | `--file-cache` | off | 不変のリモート読み取り向けにオンディスクのファイル単位キャッシュを有効にします。 |
| `CORNUS_FILE_CACHE_DIR` | `--file-cache-dir` | — | キャッシュファイル用の必須ディレクトリ。サーバーデータディレクトリとは別の専用ボリュームを使用してください。 |
| `CORNUS_FILE_CACHE_CHUNK_SIZE` | `--file-cache-chunk-size` | `1048576` | キャッシュブロックサイズ (bytes)。 |
| `CORNUS_FILE_CACHE_MAX_BYTES` | `--file-cache-max-bytes` | 無制限 | ガベージコレクションで適用するキャッシュのソフトサイズ上限。 |
| `CORNUS_BLOCK_COHERENCE` | — | classic | `subhash`、`defer`、`subfill` をカンマまたは空白で区切って指定します (`subfill` は `subhash` を暗黙に含みます)。空は classic protocol を維持します。 |
| `CORNUS_BLOCK_READAHEAD` | — | off | `subfill` 時の適応的な投機的 prefetch の bytes cap。例: `64k`、`262144`。proxy 側だけに適用されます。 |

## 認証 and API ポリシー

auth model は [Auth and TLS](/ja/topics/auth-and-tls) を参照してください。auth env が何も設定されていない場合、サーバーは資格情報なしの要求を受け付けます。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_AUTH_TOKEN` | — | — | 資格情報として受け付ける静的 bearer トークン。 |
| `CORNUS_TLS_CERT` | `--tls-cert` | — | PEM 証明書ファイル。`--tls-key` と一緒に設定すると HTTPS で提供します。 |
| `CORNUS_TLS_KEY` | `--tls-key` | — | PEM private-key ファイル。`--tls-cert` と一緒に設定すると HTTPS で提供します。 |
| `CORNUS_TLS_CLIENT_CA` | `--tls-client-ca` | — | クライアント証明書を検証する PEM CA bundle (mTLS)。verified cert の CommonName が呼び出し元 ID になります。cert の提示は任意のままです。 |
| `CORNUS_JWT_ISSUER` | — | — | 期待する JWT `iss` claim。 |
| `CORNUS_JWT_AUDIENCE` | — | — | 期待する JWT `aud` claim (クライアントの `kube-auth.audience` と一致する必要があります)。 |
| `CORNUS_JWT_HS256_SECRET` | — | — | HS256-signed JWT を検証する共有シークレット。 |
| `CORNUS_JWT_PUBLIC_KEY` | — | — | asymmetric JWT を検証する PEM 公開鍵へのパス (RSA→RS256、ECDSA→ES256)。 |
| `CORNUS_JWT_JWKS_FILE` | — | — | JWT verification 用ローカル JWKS document へのパス。 |
| `CORNUS_JWT_JWKS_URL` | — | — | JWT verification 用リモート JWKS エンドポイントの URL。 |
| `CORNUS_API_POLICY` | — | — | `/.cornus/v1/*` 対象範囲向けの per-identity 認可ポリシー。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | auth がそれ以外で有効な場合でも、レジストリからの unauthenticated プルを許可します。 |
| `CORNUS_CLIENT_TOKEN` | — | — | caretaker Docker-API プロキシがクライアントデプロイ API を操作するための client-scoped トークン。 |
| `CORNUS_CLIENT_TOKEN_SECRET` | — | — | client-scoped トークンを保持する Kubernetes シークレット参照 (`name/key`)。ワークロードの `docker:` block を有効化するために必要です。 |
| `CORNUS_CARETAKER_TOKEN` | — | — | caretaker (sidecar) callback をサーバーに対して認証するトークン。 |
| `CORNUS_CARETAKER_TOKEN_SECRET` | — | — | caretaker トークンを保持する Kubernetes シークレット参照。 |
| `CORNUS_CARETAKER_TLS_SECRET` | — | — | caretaker 用 TLS material を保持する Kubernetes シークレット。 |

## レジストリ

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | ファイルシステム | [ストレージ](#storage) / [ストレージ backends](/ja/reference/storage-backends) を参照。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | 匿名レジストリプルを許可します ([認証](#authentication-and-api-policy) を参照)。 |
| `CORNUS_REGISTRY_MIRROR` | — | — | ローカルレジストリのミスを、指定したアップストリームホスト (例: `docker.io`) へのプルスルー proxy に変えます。 |
| `CORNUS_REGISTRY_MIRROR_CACHE` | — | on | ミラーから取得したコンテンツをローカルストアに永続化します (プルスルーキャッシュ)。 |
| `CORNUS_REGISTRY_SOURCE` | — | ホストバックエンドでは `host-native` | 独立した CAS の代わりに、デプロイバックエンド自身のローカルイメージストアを `/v2/*` 経由で再エクスポートします。`host-native` は `dockerhost` バックエンドではローカル Docker デーモンに、`containerd` バックエンドではホスト containerd ストアに解決され、これらのホストバックエンドでは **既定** です。`off` は従来の永続 CAS を強制します。`--storage` を指定しない場合、レジストリは **独立したコンテンツストアを保持しません** 。`CORNUS_REGISTRY_MIRROR` とは相互排他です。[ローカルイメージストアの再利用](#reusing-a-local-image-store) を参照。 |

### ローカルイメージストアの再利用 {#reusing-a-local-image-store}

**ローカルの Docker または containerd ホスト** に対して開発するとき、イメージは
たいてい既にローカルにあります (`docker build` / `docker pull` 由来、または cornus
ビルド由来) 。そのため、別個の cornus レジストリにもう 1 つコピーを保持するのは
冗長です。そこでホストバックエンドでは、cornus の `/v2/*` レジストリは **そのローカル
ストアのビューを既定とし** ます — `CORNUS_REGISTRY_SOURCE=host-native` であり、
バックエンドごとに解決されます。どちらの場合も (`--storage` を指定しなければ) 独立した
CAS は保持されず、`_catalog` / タグ一覧はローカルストアのみを反映し、イメージの
ライフサイクルはランタイムの仕事です (`docker image prune` など) 。

- `containerd` では、`/v2/*` はホスト containerd の **ネイティブなコンテンツストア** に
  直接支えられます — 完全な **読み書き可能** なビューです。`/v2/*` へ push する
  `cornus build` はそのストアへ直接インポートし (digest 単位のブロブ + イメージレコード) 、
  イメージは即座にデプロイ可能になります。プルはそこから再エクスポートします。
  ビルドワーカーの設定は不要です。
- `dockerhost` では、`/v2/*` はローカル Docker デーモンの **読み取り専用** ビューです。
  マニフェスト/ブロブのミスは `docker save` 経由で提供され、デーモンが既に持つイメージの
  デプロイはレジストリプルをスキップします。従来の Docker には digest でアドレス指定して
  ブロブ単位に書き込めるコンテンツストアがないため、`/v2/*` への **push は `405` で拒否**
  されます — 代わりに `cornus build` がサーバー経由でルートされ、サーバーが結果を
  `docker load` でデーモンへ取り込みます。(そのため in-process な push ではなく、
  サーバーに対して `cornus build` / `cornus compose build` でビルドします。)

代わりに従来の push 可能な CAS レジストリを維持するには、**`CORNUS_REGISTRY_SOURCE=off`**
を設定するか、明示的な **`--storage`** を渡します (CAS を一次レイヤーとして保持し、ミス時
のみ再エクスポートするユニオンビュー) 。設定済みの `CORNUS_REGISTRY_MIRROR`、または
非ホストバックエンド (`bare`/`kubernetes`) も従来の CAS を維持します。

ローカル開発向けであり、高ファンアウトの共有レジストリ向けではありません。`dockerhost`
ビューに関する 1 つの注意点: `docker save` はダイジェストを再計算するため、先行する push
で得たマニフェストダイジェストは再エクスポートされたものと異なる場合があります — タグで
プルしてください。(`containerd` ビューはネイティブなコンテンツストアを読むため、
ダイジェストは保持されます。)

## Garbage collection

space は `POST /.cornus/v1/gc` による必要に応じてと、任意で periodic に回収されます。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_GC_INTERVAL` | — | 無効 | バックグラウンド storage-GC scheduler の Go duration (例: `1h`)。unset では無効。malformed または non-positive 値は startup エラーです。複数レプリカが 1 つの `s3://` ストアを共有する場合は、最大 1 レプリカで有効化してください。 |
| `CORNUS_GC_LEASE` | — | 無効 | 定期 GC 用の Kubernetes `coordination.k8s.io` Lease によるリーダー選出を有効化します (`namespace/name`、または既定 `cornus-gc` の `kube`)。`CORNUS_GC_INTERVAL` の設定が必要です。 |

## ビルドエンジン

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_BUILD_WORKER` | — | プロセス内 BuildKit | ビルドワーカーを選択します。`containerd` は execution、snapshot、content をホスト containerd に委譲します。 |
| `CORNUS_BUILD_CONCURRENCY` | — | `NumCPU` | concurrent な `/.cornus/v1/build` execution の許可数 (non-positive/unparseable は既定にフォールバック)。 |
| `CORNUS_MAX_BUILD_CONTEXT_BYTES` | — | — | upload されるビルドコンテキスト size の上限。 |
| `CORNUS_BUILD_CACHE_KEEP_BYTES` | — | — | GC が保持するビルドキャッシュの対象 size。 |
| `CORNUS_LAZY_BUILD` | — | off | `--build-context` dir を先行に同期する代わりに、server-wide に 9P で必要に応じて提供します (遅延ビルド)。 |
| `CORNUS_LAZY_9P` | — | — | 遅延 9P build-context / remote-snapshotter パスを tune します。 |
| `CORNUS_SNAPSHOTTER_TRACE` | — | off | リモート snapshotter の tracing を有効化します (diagnostics)。 |

## デプロイバックエンド

[デプロイ backends](/ja/reference/deploy-backends) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | デプロイバックエンドを選択します: `dockerhost`、`containerd`、`bare`、または `kubernetes` / `k8s`。Env-only (CLI フラグなし)。 |
| `CORNUS_ALLOW_BIND_SOURCES` | — | deny | host-bind マウントのソースとして許可される colon/comma-separated host-path prefix。default-deny。 |
| `CORNUS_ALLOW_PRIVILEGED` | — | deny | kubernetes バックエンドで特権ワークロードを許可します。 |
| `CORNUS_EGRESS_POLICY` | — | — | 許可されるエグレス gateway 経路を管理するサーバー側ポリシー。 |
| `CORNUS_EGRESS_GATEWAY` | — | off | このサーバーをエグレス gateway terminus として mark します。 |
| `CORNUS_CREDENTIALS_URL` | — | — | generic 資格情報配送が取得するエンドポイントとしてワークロードに通知されます (injected env var)。 |
| `CORNUS_CARETAKER_CONFIG` | — | — | caretaker sidecar/companion に渡される JSON caretaker 役割設定。 |
| `CORNUS_AGENT_IMAGE` | — | — | クラスター内 mount/deploy エージェントに使うイメージ。 |
| `CORNUS_AGENT_DIR` | — | — | client-agent artifact 用ディレクトリ (クライアント側)。 |

### Containerd バックエンド

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_CONTAINERD_ADDRESS` | — | `/run/containerd/containerd.sock` | Containerd ソケット (標準の `CONTAINERD_ADDRESS` もフォールバックとして尊重されます)。 |
| `CORNUS_CONTAINERD_NAMESPACE` | — | `cornus` | ワークロード用 containerd 名前空間。 |
| `CORNUS_CONTAINERD_SNAPSHOTTER` | — | `overlayfs` | Rootfs snapshotter (overlay-backed ホストでは `native` を設定)。 |
| `CORNUS_CONTAINERD_INSECURE_REGISTRIES` | — | `localhost` のみ | イメージプル時に plain-HTTP として扱う comma-separated `host[:port]`。 |
| `CORNUS_CONTAINERD_LOG_MAX_BYTES` | — | 16 MiB | ログ rotation size (古い generation を 1 つ保持)。 |
| `CORNUS_CNI_BIN_DIR` | — | `/opt/cni/bin` (also `CNI_PATH`) | CNI プラグインを検出するディレクトリ。 |
| `CORNUS_CNI_SUBNET_BASE` | — | `10.4` | compose ネットワークごとに切り出す `/24` の base。 |
| `CORNUS_DOCKER_SOCK` | — | `/var/run/docker.sock` | `dockerhost` バックエンド用 Docker ソケット (クライアントの `cornus daemon docker` listen ソケットでもあります)。 |

### Bare バックエンド

デーモンレスバックエンド (`CORNUS_DEPLOY_BACKEND=bare`)。上記の `CORNUS_CNI_*` を `containerd` と共有します。デーモンソケットは不要です。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_BARE_RUNTIME` | — | `runc` | 直接駆動する OCI ランタイムバイナリ (`runc`、`crun`、`youki`、または gVisor 用の `runsc` — runc-CLI 互換の任意のバイナリ)。起動時に検証されます。 |
| `CORNUS_BARE_STATS_SOURCE` | — | 自動 (ランタイム名で判定) | `Stats` がメトリクスを読む先: `runtime` (`runc events --stats`) か `cgroup` (host cgroup ファイル)。既定はランタイムの basename で決まります — `runsc`/`gvisor` はサンドボックス化されているため `runtime`、`runc`/`crun`/`youki` は `cgroup`。名前が特殊なインストールではこの項目で上書きします。 |
| `CORNUS_BARE_SNAPSHOTTER` | — | overlay (native フォールバック) | Rootfs snapshotter。overlay-on-overlay を拒否する overlay-backed / docker-in-docker ホストでは `native` を設定します。 |
| `CORNUS_BARE_INSECURE_REGISTRIES` | — | `localhost` のみ | イメージプル時に plain-HTTP として扱う comma-separated `host[:port]`。 |
| `CORNUS_BARE_SYSTEMD_CGROUP` | — | off (cgroupfs) | ランタイムを systemd cgroup driver に切り替えます (既定は cgroupfs。runc が v1/v2 で直接管理します)。 |
| `CORNUS_BARE_DNS` | — | on | netns gateway 上で guest container DNS に応答するプロセス内 resolver。false 値で無効化し、hosts-file 解決のみにフォールバックします。 |
| `CORNUS_KNATIVE_STRICT` | — | `false` | クラスターが `serving.knative.dev/v1` を提供しないとき、警告付きの通常 Deployment として実行する代わりに Knative 有効デプロイを失敗させます。 |
| `CORNUS_BARE_SHIM` | — | off | container ごとの監督 shim (cornus の conmon 相当。cornus 再起動後も存続) をオプトインします。off では既定のプロセス内 supervisor を使います。 |
| `CORNUS_BARE_REMOTE` | — | off | `bare` バックエンドを常時オンのインスタンスごと remote-companion sidecar にオプトインします (`CORNUS_CONTAINERD_REMOTE` と同じ)。companion が client-local mount を行い、`cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent` を再ルートします。`CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL` が必要です。 |

### Kubernetes バックエンド

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_K8S_NAMESPACE` | — | クラスター内 / current | kubernetes バックエンドがデプロイする名前空間。 |
| `CORNUS_K8S_NET_DRIVER` | — | `services` | user ネットワークの既定ネットワーク driver (`services`、`bridge`、`ipvlan`、`macvlan`、`cilium`)。 |
| `CORNUS_K8S_NET_STRICT` | — | `false` | 要求されたネットワーク fabric を実現できない場合に、degrade ではなく fail します。 |
| `CORNUS_K8S_POLICY_CNI` | — | `false` | policy-capable CNI 上で NetworkPolicy-based isolation を有効化します。 |
| `CORNUS_K8S_IMAGE_PULL_POLICY` | — | バックエンド既定 | pod `imagePullPolicy` を上書きします。 |
| `CORNUS_K8S_SIDECAR_IMAGE` | — | the cornus イメージ | caretaker sidecar に使うイメージ。 |

### イングレス defaults

[イングレス](/ja/topics/ingress) に opt in するワークロード向けのサーバー側フォールバックです (kubernetes バックエンド)。Helm `ingress.*` 値としても設定できます。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | — | — | `<name>.<domain>` ホストを auto-derive する base wildcard ドメイン。空の場合、ワークロードは自分のホストまたはドメインを設定する必要があります。 |
| `CORNUS_INGRESS_CLASS` | — | クラスター既定 | 作成されるイングレスの既定 `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | — | — | TLS-enabled イングレス用既定 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | — | `false` | true (かつドメインが設定済み) の場合、resolved ホストがドメイン外に出るワークロードを拒否します。 |

## Tunnels

[パブリックトンネル](/ja/topics/tunnels) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_TUNNEL_BACKEND` | — | `ngrok` | Public-URL トンネルバックエンド: `ngrok` (既定)、`ssh` (SSH reverse-tunneling)、`cloudflare` (Cloudflare トンネル)、または `tailscale` (Tailscale Funnel)。 |
| `CORNUS_TUNNEL_AUTHTOKEN` | — | — | クライアントが資格情報を省略した場合に使われる、選択したトンネルバックエンドのサーバー側既定資格情報。同じ変数名は、クライアント自身の環境で設定した場合、クライアントの `cornus tunnel --authtoken` フラグの値にもなります — 同じ名前で 2 つの異なるプロセスに使われますが、値の種類は同じです。 |
| `CORNUS_TUNNEL_CLOUDFLARED_BIN` | — | `cloudflared` on パス | `cloudflared` binary へのパス。 |
| `CORNUS_TUNNEL_TAILSCALE_BIN` | — | `tailscale` on パス | `tailscale` binary へのパス。 |
| `CORNUS_TUNNEL_SSH_ADDR` | — | — | SSH トンネルサーバーアドレス。 |
| `CORNUS_TUNNEL_SSH_USER` | — | — | SSH トンネル user。 |
| `CORNUS_TUNNEL_SSH_BIND` | — | — | SSH reverse トンネルのリモートバインドアドレス。 |
| `CORNUS_TUNNEL_SSH_URL_TEMPLATE` | — | — | SSH トンネルから導出するパブリック URL の template。 |
| `CORNUS_TUNNEL_SSH_URL_FROM_SESSION` | — | off | SSH セッション出力からパブリック URL を導出します。 |
| `CORNUS_TUNNEL_SSH_HOSTKEY` | — | — | expected SSH ホストキー。 |
| `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` | — | — | SSH ホスト verification 用 `known_hosts` ファイルへのパス。 |
| `CORNUS_TUNNEL_SSH_INSECURE` | — | off | SSH host-key verification をスキップします (testing のみ)。 |

## Hub (ワークロード間オーバーレイ)

[ワークロード間 hub](/ja/topics/hub) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_HUB_STORE` | — | メモリ内 | Hub catalog ストア。`kube` は Kubernetes-backed ストアを使います。 |
| `CORNUS_HUB_REDIS` | — | — | 分散型ハブストア用の Redis URL (レプリカ間カタログを有効化)。 |
| `CORNUS_HUB_FORWARD_URL` | — | — | レプリカが hub 中継トラフィックを転送する URL。 |
| `CORNUS_HUB_FORWARD_CA` | — | — | hub 転送エンドポイントを検証する PEM CA bundle。 |
| `CORNUS_HUB_POLICY` | — | — | どの ID がどの hub サービスに到達できるかを管理するポリシー。 |
| `CORNUS_HUB_REGISTER_POLICY` | — | — | どの ID が hub サービスを登録 (エクスポート) できるかを管理するポリシー。 |

## オブザーバビリティ

オブザーバビリティ model は [アーキテクチャ overview](/ja/architecture/) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_OTEL` | `--otel` | off | 標準 `OTEL_*` env による OpenTelemetry (trace/metric/log) を有効化します。`OTEL_*` exporter/endpoint env var が設定されている場合も暗黙に有効化されます。 |
| `CORNUS_METRICS_PROMETHEUS` | — | off | Prometheus metrics エンドポイントを公開します (OpenTelemetry が有効な場合のみ有効)。 |

同じ `CORNUS_OTEL` / `OTEL_*` gate は **クライアント CLI** の tracing も有効化します。`cornus` を実行する環境に設定すると、各 invocation が root span を emit し、W3C `traceparent` をサーバー (さらに caretaker) へ伝搬します。そのため `cornus deploy` / `cornus build` / `cornus compose up` は isolated サーバー span ではなく、1 つの end-to-end トレースとして見えます。

## クライアント側変数 (for 参照)

これらはサーバーではなく CLI が読みますが、同じ `CORNUS_*` 名前空間にあります。[接続設定](/ja/reference/connection-config) と [リモート workflows](/ja/topics/remote-workflows) を参照してください。

| 変数 | 既定 | Meaning |
| --- | --- | --- |
| `CORNUS_SERVER` / `CORNUS_HOST` | selected プロファイル, then `http://localhost:5000` | クライアントコマンド用リモート cornus サーバー URL。 |
| `CORNUS_TOKEN` | — | クライアント要求用 bearer トークン (プロファイルの `token` を上書き)。 |
| `CORNUS_CONFIG` | platform 設定パス | クライアント [接続設定](/ja/reference/connection-config) ファイルへのパス。 |
| `CORNUS_CONTEXT` | 設定 `current-context` | 使用する接続プロファイル。 |
| `CORNUS_OUTPUT` | `auto` | 出力 rendering モード (`auto`、`plain`、`fancy`、`json`)。[出力 modes](/ja/guides/output-modes) を参照。 |
| `CORNUS_CONDUIT` | プロファイル / `port-forward` | セッション conduit モード (`port-forward` または `socks5`)。 |
| `CORNUS_VIA_SERVER` | プロファイル / 直接 | ワークロード streaming をサーバープロキシ経由にします。 |
| `CORNUS_BUILDER` | — | delegated ビルド用リモートビルドエンドポイント。 |
| `CORNUS_REGISTRY` | server-advertised ホスト | レジストリ part を持たないタグ用レジストリホスト (リモートビルド)。 |
