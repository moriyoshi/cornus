# サーバー環境変数

このページは、[`cornus serve`](/ja/cli/serve) とサーバーサブシステムが読む `CORNUS_*` 環境変数を一覧します。一部は `cornus serve` のフラグに対応します (下に記載)。多くは環境変数だけで設定する項目であり、サーバー、デプロイバックエンド、ビルドエンジン、トンネルから直接読み取られます。

::: info
この list はソース tree (`grep 'CORNUS_[A-Z0-9_]+' pkg cmd`) から導いた実用リファレンスです。内部または変化中の knob が少し含まれる場合があります。authoritative な挙動は常に code にあります。test-only 変数 (`CORNUS_TEST_*`) は省略しています。CLI が消費するクライアント側変数 (サーバーではない) は最後に別 group としてまとめています。
:::

## General / リスナー

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_ADDR` | `--addr` | `:5000` | `/v2/*` と `/.cornus/v1/*` の HTTP listen アドレス。コンテナ化された caretaker がサーバーへ逆方向に接続するため、既定では全インターフェースです。ワークロードがサーバーに到達する必要がない場合は、`CORNUS_ADDR=127.0.0.1:5000` を設定してこのマシンだけに制限できます。[Listen アドレスと公開範囲](/ja/cli/serve#listen-アドレスと公開範囲) を参照してください。 |
| `CORNUS_DATA` | — | platform データディレクトリ | サーバーデータディレクトリ (レジストリファイルシステムストア、upload、バックエンド状態)。 |
| `CORNUS_ROOTLESS` | `--rootless` | off | ビルドエンジンをルートレスモード (user 名前空間) で実行します。 |
| `CORNUS_BUILDER_URL` | `--builder-url` | — | プロセス内でビルドする代わりに、上流の cornus ビルダー (例: `ws://127.0.0.1:5099`) にビルドを委譲します。[ビルダーへのビルドの委譲](#delegating-builds-to-a-builder) を参照してください。 |
| `CORNUS_BUILDER_AUTO` | `--[no-]builder-auto` | 有効 | プロセス内エンジンが動作できず `--builder-url` も未設定の場合に、特権付きの cornus ビルダーコンテナを自動的に起動します。 |
| `CORNUS_BUILDER_IMAGE` | `--builder-image` | 自己ビルド | 実行中のバイナリからビルドする代わりに、公開イメージをビルダーとして固定します。 |
| `CORNUS_BUILDER_BASE_IMAGE` | `--builder-base-image` | ホストのディストリビューション | 自己ビルドするビルダーイメージのベースイメージ。 |
| `CORNUS_LOG_LEVEL` | — | `info` | ログ verbosity (`debug`、`info`、`warn`、`error`)。 |
| `CORNUS_ADVERTISE_URL` | — | — | マウントエージェントや caretaker sidecar が接続し直す cornus URL。kubernetes バックエンドでクライアントローカルマウントに必要で、`dockerhost`/`containerd` でも `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` で同じ sidecar 経路にオプトインした場合に必要です。実際に接続し直すものがある場合にのみ要求されます。同一ホスト上でクライアントローカルマウントを自ら実現する co-located なサーバーや、デプロイ時に解決する [`env` 種別の資格情報](/ja/guides/credentials)には、本変数も `CORNUS_AGENT_IMAGE` も不要です。 |
| `CORNUS_ADVERTISE_REGISTRY` | — | derived | デプロイ対象がプルできるレジストリとしてサーバーがクライアントへ通知する `host[:port]` (および任意の scheme) を上書きします (`GET /.cornus/v1/info`)。 |
| `CORNUS_ACTIVITY_MAX_BYTES` | — | `8388608` | フライトレコードのログ (`<data dir>/activity`) のサイズ上限。直前の世代を 1 つだけ併せて保持します。[cornus activity](/ja/cli/activity) を参照してください。 |
| `CORNUS_HOST_PATH_MAP` | — | 自動検出 | `container-path=host-path` の組をカンマ区切りで指定し、このサーバーのパスがホスト上でどう見えるかを宣言します。ホストの docker/containerd に対して **コンテナ内** で動作する cornus 向けです。サーバー自身が判別できない場合にのみ必要です (docker 以外のランタイムや、検査できないコンテナ)。明示したエントリーは常に検出されたものより優先されます。不正な値は起動時エラーになります。[サーバーをコンテナで実行する](/ja/guides/server-in-a-container) を参照してください。 |
| `CORNUS_HOST_NETWORK` | — | 自動検出 | `1`/`0`。このサーバーのコンテナがホストのネットワーク名前空間を共有しているかどうかを明示し、自己問い合わせの結論を上書きします。CNI を使うホストバックエンド (`containerd`、`bare`) では、独立した名前空間は公開ポートがサーバー自身のコンテナ内部で NAT され、ホストからは見えないことを意味します。そのためサーバーがそれを検出した場合は起動を拒否します。それを承知の上で起動するには `0` を、すでにホストネットワークがあるのに判別できない場合は `1` を設定してください。`bare` には問い合わせ先のデーモンがないため、これが唯一の答えになります。不正な値は起動時エラーになります。[サーバーをコンテナで実行する](/ja/guides/server-in-a-container) を参照してください。 |
| `CORNUS_REPLICA_ID` | — | — | このレプリカの固定 ID。分散型ハブストアと GC のリーダー選出による制御で使われ、自動発行するレプリカ間転送 JWT の `sub` と `kid` にもなります。 |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | デプロイバックエンド: `dockerhost`、`podman`、`kubernetes` (`k8s`)、`containerd`、`bare`、または `incus`。認識できない値は起動時エラーになります。`docker` のような近似値を受け入れると、`dockerhost` を選びながらレジストリをホストネイティブ再エクスポートから外してしまうためです。 |

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

## 認証と API ポリシー {#authentication-and-api-policy}

auth model は [セキュリティと認証](/ja/guides/security) を参照してください。auth env が何も設定されていない場合、サーバーは資格情報なしの要求を受け付けます。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_AUTH_TOKEN` | — | — | 資格情報として受け付ける静的 bearer トークン。 |
| `CORNUS_INSTALLATION_SECRET` | — | `CORNUS_DATA` の下に生成 | インストール全体で共有する内部署名キー。クライアント認証が有効な場合に限り、Cornus 自身のレジストリへのビルドとプル用に短時間有効な範囲限定資格情報を発行します。すべてのレプリカに同じ値を設定してください。この値だけではクライアント認証は有効になりません。 |
| `CORNUS_AUTH_KEYSTORE` | — | 自動 | SSH 鍵登録ストアのモード。`file` は `<CORNUS_DATA>/auth/authorized_keys` に書き込み、`none` は宣言的な鍵を維持したまま実行時登録を無効にします。未設定の場合、鍵認証が別の方法で設定されているか、ストアがすでに存在するときだけファイルストアが有効になります。 |
| `CORNUS_AUTHORIZED_KEYS` | — | — | 改行で区切った OpenSSH `authorized_keys` エントリー。設定すると SSH 公開鍵によるクライアント認証が有効になります。この宣言的なエントリーは API からは読み取り専用です。 |
| `CORNUS_TLS_CERT` | `--tls-cert` | — | PEM 証明書ファイル。`--tls-key` と一緒に設定すると HTTPS で提供します。 |
| `CORNUS_TLS_KEY` | `--tls-key` | — | PEM private-key ファイル。`--tls-cert` と一緒に設定すると HTTPS で提供します。 |
| `CORNUS_TLS_CLIENT_CA` | `--tls-client-ca` | — | クライアント証明書を検証する PEM CA bundle (mTLS)。verified cert の CommonName が呼び出し元 ID になります。cert の提示は任意のままです。 |
| `CORNUS_JWT_ISSUER` | — | — | 期待する JWT `iss` claim。 |
| `CORNUS_JWT_AUDIENCE` | — | — | 期待する JWT `aud` claim (クライアントの `kube-auth.audience` と一致する必要があります)。 |
| `CORNUS_JWT_HS256_SECRET` | — | — | HS256-signed JWT を検証する共有シークレット。 |
| `CORNUS_JWT_PUBLIC_KEY` | — | — | asymmetric JWT を検証する PEM 公開鍵へのパス (RSA→RS256、ECDSA→ES256)。 |
| `CORNUS_JWT_JWKS_FILE` | — | — | JWT verification 用ローカル JWKS document へのパス。 |
| `CORNUS_JWT_JWKS_URL` | — | — | JWT verification 用リモート JWKS エンドポイントの URL。 |
| `CORNUS_JWT_SCOPE_MAP` | — | — | YAML/JSON のスコープマップへのパス。サードパーティの issuer のクレームを cornus のスコープに変換するルール群です。cornus の `scope` クレームを持たない JWKS 検証済みトークンには必須です。[スコープマッピング](/ja/guides/security#サードパーティのクレームを-cornus-のスコープにマッピングする) を参照してください。 |
| `CORNUS_JWT_DEFAULT_SCOPE` | — | — | `CORNUS_JWT_SCOPE_MAP` のどのルールにも一致しなかったトークンに与えるキャッチオールのスコープ (例: `api`)。マップの後ろに追加されるため、明示的なルールが引き続き決定権を持ちます。 |
| `CORNUS_API_POLICY` | — | — | `/.cornus/v1/*` 対象範囲向けの per-identity 認可ポリシー。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | auth がそれ以外で有効な場合でも、レジストリからの unauthenticated プルを許可します。 |
| `CORNUS_CLIENT_TOKEN` | — | — | caretaker Docker-API プロキシがクライアントデプロイ API を操作するための client-scoped トークン。 |
| `CORNUS_CLIENT_TOKEN_SECRET` | — | — | client-scoped トークンを保持する Kubernetes シークレット参照 (`name/key`)。ワークロードの `docker:` ブロックを有効化するために必要です。 |
| `CORNUS_CARETAKER_TOKEN` | — | — | caretaker (sidecar) callback をサーバーに対して認証するトークン。 |
| `CORNUS_CARETAKER_TOKEN_SECRET` | — | — | caretaker トークンを保持する Kubernetes シークレット参照。 |
| `CORNUS_CARETAKER_TLS_SECRET` | — | — | caretaker 用 TLS material を保持する Kubernetes シークレット。 |

## レジストリ

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | ファイルシステム | [ストレージ](#ストレージ) / [ストレージ backends](/ja/reference/storage-backends) を参照。 |
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

### ビルダーへのビルドの委譲 {#delegating-builds-to-a-builder}

プロセス内のビルドエンジンは非特権では動作しません。BuildKit はすべてのスナップショットをマウントし、`mount(2)` には `CAP_SYS_ADMIN` が必要なため、非特権の `cornus serve` はすべてのビルドに失敗します。多くの場合、Dockerfile の読み取り中の `lchown ...: operation not permitted`、または `failed to mount ...: operation not permitted` として現れます。`--rootless` だけではこれは変わりません。BuildKit のルートレスフラグを設定するだけで user 名前空間は作成せず、`kernel.apparmor_restrict_unprivileged_userns=1` のホスト (Ubuntu 24.04 以降) では非特権の user 名前空間自体が禁止されています。

既定ではこれは自動的に処理されます。最初のビルド時に、`mount(2)` できないサーバーは特権付きの cornus ビルダーコンテナを起動し、そこへ委譲します。

```
build engine cannot mount(2) as this user; using a containerized builder
delegating builds to containerized builder url=ws://127.0.0.1:5099
```

ビルダーイメージはプルするのではなく、**実行中のバイナリからビルドされます**。サーバーは自身の実行ファイルを Docker デーモン経由で使い捨てのイメージ `cornus-builder:<binary-hash>` にパッケージするため、ビルダーはサーバーとバイト単位で同一であり、レジストリへのアクセスを必要とせず、バージョンがずれることもありません。タグはバイナリのコンテンツハッシュなので、cornus を更新すると新しいイメージが作られ、バイナリが変わらなければ既存のものが再利用されます (最初のビルドではその作成に数秒かかりますが、以降はかかりません)。

ベースイメージは既定でホスト自身のディストリビューション (`/etc/os-release` から取得) になります。ローカルでビルドした cornus は通常ホストの libc に動的リンクされており、一致しないベースでは実行できないためです。`--builder-base-image` で上書きでき、`--builder-image` を指定すると自己ビルドの代わりに公開イメージを固定します。ベースには `runc` が必要で (BuildKit がすべての `RUN` でこれを呼び出します)、存在しない場合はイメージのビルド時にインストールされます。

コンテナ名は `cornus-builder` で、`--privileged` とホストネットワークで実行され、専用の `cornus-builder-cache` ボリュームを持ちます。サーバーのデータディレクトリは決して使いません。ビルダーは root として実行されるため、root 所有のスナップショットが残ってしまうからです。起動は遅延されるので、ビルドを行わないサーバーはビルダーを起動しません。また再起動時には重複して作らず既存のものを引き継ぐため、ビルドキャッシュは温まったまま保たれます。権限の判定は uid を調べるのではなく、実際のバインドマウントを試みて行います。root でも禁止されている場合があり、非 root でも可能な場合があるためです。

ビルダーは、このサーバーのレジストリモードも**反映します**。ビルドセッションはそのまま中継され、結果の配送方法を決めるのはビルダーだからです。

- **再エクスポートモード** (ホストバックエンドで `--storage` なし。既定): レジストリは読み取り専用なので、ビルダーはホストの Docker ソケットを共有し、完了したイメージをプロセス内ビルドと同様に同じデーモンへ `docker load` します。ビルダー自身にモードを解決させると、代わりに読み取り専用レジストリへ push し、`405 Method Not Allowed` で失敗します。
- **CAS モード** (明示的な `--storage` または非ホストバックエンド): ビルダーは自身のストレージを受け取り、結果を対象レジストリへ push します。

モード変更はビルダー設定を変えるため、既存ビルダーを再利用せず再作成します。ホストの **containerd** ストアを再エクスポートするレジストリはこの方法に対応しません。コンテナ化ビルダーから書き込めないためで、後で失敗するのではなく説明付きで拒否されます。

本来ビルドがまったく失敗するホストでのみ機能するため、すでにビルドに成功しているホストの挙動を変えることはありません。`--no-builder-auto` で無効化できます。また、到達可能な Docker デーモンが必要です。

ビルダーを自分で管理する場合は、サーバーに明示的に指定します。

```sh
docker run -d --name cornus-builder --privileged --network host \
  -v cornus-builder-cache:/var/lib/cornus \
  ghcr.io/moriyoshi/cornus:latest \
  serve --addr 127.0.0.1:5099 --storage /var/lib/cornus/registry

cornus serve --addr :5000 --builder-url ws://127.0.0.1:5099
```

ビルドの入口は両方とも委譲されます。`GET /.cornus/v1/build/attach` は生の WebSocket としてビルダーへそのまま中継され、`POST /.cornus/v1/build` はコンテキストの tar とクエリをそのまま保って転送されます。attach パスはバイト単位で中継されるため、ビルダーは**呼び出し元**のエクスポートに対して 9P を終端します。つまり呼び出し元のビルドコンテキスト、名前付きコンテキスト、シークレットが委譲元のホストに置かれることはありません。認可は、ビルダーに何かが届く前に委譲元のサーバーで従来どおり適用されます。

ビルダーを自分で実行する場合に押さえるべき点が 3 つあります (上記の自動ビルダーは 3 点すべてを処理します)。

- **`--storage` を渡すこと。** これがないとサーバーは host-native re-export を既定とし、結果をローカルの Docker デーモンに読み込ませようとしますが、ビルダーコンテナにそれはありません。その場合ビルドは成功し、エクスポート時にだけ `failed to copy to tar: read/write on closed pipe` という紛らわしいエラーで失敗します。
- **専用のデータディレクトリまたはボリュームを与えること。** ビルダーは root として実行されるため、非特権サーバーのデータディレクトリを共有すると root 所有のスナップショット (`drwx------`) が残り、非特権サーバーはそこを辿れなくなります。
- 上記のように `--network host` を推奨します。`localhost:5000/app` のようなイメージ参照が、ビルダー内とホスト上で同じ意味になるためです。

## デプロイバックエンド

[デプロイ backends](/ja/reference/deploy-backends) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | デプロイバックエンドを選択します: `dockerhost`、`podman`、`containerd`、`bare`、`incus`、または `kubernetes` / `k8s`。Env-only (CLI フラグなし)。 |
| `CORNUS_ALLOW_BIND_SOURCES` | — | deny | host-bind マウントのソースとして許可される colon/comma-separated host-path prefix。default-deny。 |
| `CORNUS_ALLOW_PRIVILEGED` | — | deny | kubernetes バックエンドで特権ワークロードを許可します。 |
| `CORNUS_EGRESS_POLICY` | — | — | 許可されるエグレス gateway 経路を管理するサーバー側ポリシー。 |
| `CORNUS_EGRESS_GATEWAY` | — | off | このサーバーをエグレス gateway terminus として mark します。 |
| `CORNUS_CREDENTIALS_URL` | — | — | generic 資格情報配送が取得するエンドポイントとしてワークロードに通知されます (injected env var)。 |
| `CORNUS_CARETAKER_CONFIG` | — | — | caretaker sidecar/companion に渡される JSON caretaker 役割設定。 |
| `CORNUS_AGENT_IMAGE` | — | — | マウント/エグレス/デプロイの caretaker sidecar や companion に使う、cornus を組み込んだイメージ — kubernetes の Pod sidecar、`dockerhost`/`containerd`/`bare` のエグレス companion、および (`CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`/`CORNUS_BARE_REMOTE` と併用した場合) 常時オンの remote companion (マウント、port-forward/tunnel の再ルート、exec の agent 転送) です。 |
| `CORNUS_AGENT_DIR` | — | — | client-agent artifact 用ディレクトリ (クライアント側)。 |
| `CORNUS_DOCKER_REMOTE` | — | off | `dockerhost` バックエンドを常時オンのインスタンスごと remote-companion sidecar にオプトインします。companion は各インスタンスのネットワーク名前空間を共有し、デプロイが `--mount` を使うかどうかにかかわらず作成されます — このサーバーと同じホストにない Docker デーモン (例: `DOCKER_HOST=tcp://...`) のためのモードです。クライアントローカルマウントは既定の単一ホスト kernel-9p 高速パスではなく companion (`rshared`/`rslave` propagation を持つ Docker ボリューム) 経由で実現され、`cornus port-forward`/`cornus tunnel` と `cornus exec --forward-agent` はサーバーがインスタンスへ直接接続する代わりに companion 経由で再ルートされます。`CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL` が必要です。[デプロイ backends](/ja/reference/deploy-backends) を参照してください。 |
| `CORNUS_PODMAN_SOCKET` | — | なし | `podman` バックエンドが使う Podman API エンドポイント。パス、`unix://` / `tcp://` URL、または `ssh://` の接続先を指定します。**既定値はなく**、`CONTAINER_HOST` / `DOCKER_HOST` や既定のソケットパスを探しに行くこともしません。本変数と `CORNUS_PODMAN_SERVICE` のどちらも設定されていない場合、サーバーは起動を拒否します。これにより、どのデーモンを駆動したのかを常に設定から答えられます。両方を設定するとエラーです。 |
| `CORNUS_PODMAN_SERVICE` | — | off | cornus 自身が `podman system service` を専用ソケット上で実行し、監督します。`podman.socket` ユニットを有効化する必要はありません。`PATH` 上の `podman` バイナリだけが必要です。`CORNUS_PODMAN_SOCKET` とは排他です。 |
| `CORNUS_PODMAN_REMOTE` | — | off | `dockerhost` に対する `CORNUS_DOCKER_REMOTE` と同じく、`podman` バックエンドをインスタンスごとの常時オン remote companion にオプトインさせます。**ルートレスな podman に対する `cornus port-forward` / `cornus tunnel` には必須です**。ルートレスのワークロードの名前空間はホストから経路がないため、本変数がないとこれらのコマンドはタイムアウトを待たずに即座に拒否します。`CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL` も必要です。 |

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
| `CORNUS_CONTAINERD_REMOTE` | — | off | `containerd` バックエンドを `CORNUS_DOCKER_REMOTE` と同じ常時オンのインスタンスごと remote-companion sidecar にオプトインします。companion は各インスタンスの pin されたネットワーク名前空間に参加し、デプロイが `--mount` を使うかどうかにかかわらず作成されます (companion のコンテナ/タスクが kernel 9P マウントを行い、`rshared`/`rslave` OCI マウントオプションを持つ共有ホストディレクトリ経由で中継します。`cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent` も companion 経由で再ルートされます)。containerd 自体がリモートから到達可能になるわけでは**ありません** (クライアント dialer は unix ソケット専用です) — 依然として同じホストにあるデーモンに対して port-forward / exec の agent 転送をどう実現するかが変わり、さらにこのバックエンドでクライアントローカルマウントを利用可能にするのはこの変数です。`dockerhost`/`bare` と違って戻れる kernel-9p 高速パスがないため、未設定のまま `--mount` を伴うデプロイを行うと拒否されます (エラーがこの変数を名指しします) 。`CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL` が必要です。[デプロイ backends](/ja/reference/deploy-backends) を参照してください。 |
| `CORNUS_DOCKER_SOCK` | — | `$XDG_RUNTIME_DIR/cornus-docker.sock` | クライアント側の [`cornus daemon docker`](/ja/cli/daemon) プロキシが**listen する** Unix ソケット。`dockerhost` バックエンドの設定には**使われません**。バックエンドは `DOCKER_HOST` を読みます。 |

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
| `CORNUS_BARE_SHIM` | — | off | container ごとの監督 shim (cornus の conmon 相当。cornus 再起動後も存続) をオプトインします。off では既定のプロセス内 supervisor を使います。 |
| `CORNUS_BARE_REMOTE` | — | off | `bare` バックエンドを常時オンのインスタンスごと remote-companion sidecar にオプトインします (`CORNUS_CONTAINERD_REMOTE` と同じ)。companion が client-local mount を行い、`cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent` を再ルートします。`CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL` が必要です。 |

### Incus バックエンド

OCI イメージを Incus のアプリケーションコンテナとして実行する Incus バックエンド (`CORNUS_DEPLOY_BACKEND=incus`)。Incus 6.3+ と、**デーモン**ホスト上の `skopeo` + `umoci` が必要です。`CORNUS_CNI_*` は一切使いません。インスタンスのネットワークは incusd が所有します。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_INCUS_SOCKET` | — | `/var/lib/incus/unix.socket` | Incus デーモンの unix ソケット。 |
| `CORNUS_INCUS_PROJECT` | — | `default` | インスタンスを作成する Incus プロジェクト。 |
| `CORNUS_INCUS_STORAGE_POOL` | — | `default` | バックエンドがカスタムボリュームを作成する Incus ストレージプール。デプロイメントの管理対象 `volumes` と、リモートモードでは companion の共有エージェントボリュームが対象です。どちらもないデプロイメントはプールに一切触れません。インスタンスはプロジェクトのプロファイルからルートディスクを取ります。 |
| `CORNUS_INCUS_INSECURE_REGISTRIES` | — | ループバックのみ | incusd にイメージ参照を渡す際、平文 HTTP で扱う comma/space-separated `host[:port]`。incusd は `skopeo` 経由でプルし、skopeo は平文 HTTP のレジストリを拒否するため、デーモンホスト側にも対応する `/etc/containers/registries.conf.d/` エントリが必要です。 |
| `CORNUS_INCUS_REMOTE` | — | off | `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`/`CORNUS_BARE_REMOTE` がそれぞれのバックエンドで行うのと同じく、caretaker companion の経路を有効にします。各レプリカに PortForward と AgentRelay の caretaker role を持つ companion インスタンスが付き、ポート転送のトラフィックはインスタンスのアドレスへ直接接続する代わりにそこを経由して再ルートされ、`cornus exec --forward-agent` が使えるようになります (これなしでは前段で拒否されます)。`CORNUS_AGENT_IMAGE` と `CORNUS_ADVERTISE_URL` が必要で、どちらかが欠けているとデプロイは前段で失敗します。クライアントローカルマウントやエグレスはもたらしません — [デプロイバックエンド](/ja/reference/deploy-backends#incus) を参照してください。 |

### Kubernetes バックエンド

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_K8S_NAMESPACE` | — | クラスター内 / current | kubernetes バックエンドがデプロイする名前空間。 |
| `CORNUS_KUBE_QPS` | — | `50` | Kubernetes クライアントのリクエストレート上限 (1 秒あたりのクエリ数)。並行するデプロイ処理と readiness 処理でのクライアント側スロットリングを調整するには、この値を増減します。 |
| `CORNUS_KUBE_BURST` | — | `100` | Kubernetes クライアントのレートリミッターのバースト容量。 |
| `CORNUS_K8S_NET_DRIVER` | — | `services` | user ネットワークの既定ネットワーク driver (`services`、`bridge`、`ipvlan`、`macvlan`、`cilium`)。 |
| `CORNUS_K8S_NET_STRICT` | — | `false` | 要求されたネットワーク fabric を実現できない場合に、degrade ではなく fail します。 |
| `CORNUS_K8S_POLICY_CNI` | — | `false` | policy-capable CNI 上で NetworkPolicy-based isolation を有効化します。 |
| `CORNUS_K8S_IMAGE_PULL_POLICY` | — | バックエンド既定 | pod `imagePullPolicy` を上書きします。 |
| `CORNUS_K8S_SIDECAR_IMAGE` | — | the cornus イメージ | caretaker sidecar に使うイメージ。 |
| `CORNUS_KNATIVE_STRICT` | — | `false` | クラスターが `serving.knative.dev/v1` を提供しないとき、警告付きの通常 Deployment として実行する代わりに Knative 有効デプロイを失敗させます。 |

### イングレス defaults

[イングレス](/ja/guides/ingress) に opt in するワークロード向けのサーバー側フォールバックです (kubernetes バックエンド)。Helm `ingress.*` 値としても設定できます。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | — | — | `<name>.<domain>` ホストを auto-derive する base wildcard ドメイン。空の場合、ワークロードは自分のホストまたはドメインを設定する必要があります。 |
| `CORNUS_INGRESS_CLASS` | — | クラスター既定 | 作成されるイングレスの既定 `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | — | — | TLS-enabled イングレス用既定 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | — | `false` | true (かつドメインが設定済み) の場合、resolved ホストがドメイン外に出るワークロードを拒否します。 |
| `CORNUS_INGRESS_LISTEN` | — | — | サーバー自身のイングレスフロントドアをこのアドレス (例 `:8080`) にバインドし、サーバーが接続されているネットワーク上で宣言済みのホストとパスを提供します。空の場合、フロントドアは[`cornus ingress-tunnel`](/ja/cli/ingress-tunnel)経由でのみ到達可能です。バインド失敗はログに記録されるだけで、致命的ではありません。 |
| `CORNUS_INGRESS_CONTROLLER` | — | 自動検出 | イングレストンネルがトラフィックを渡すクラスターイングレスコントローラー Service を `<namespace>/<service>[:httpPort/httpsPort]` 形式で指定します。空の場合は既知の名前から検出します。 |

## Tunnels

[トンネル](/ja/guides/tunnels) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_TUNNEL_BACKEND` | — | `ngrok` | Public-URL トンネルバックエンド: `ngrok` (既定)、`ssh` (SSH reverse-tunneling)、`cloudflare` (Cloudflare トンネル)、または `tailscale` (Tailscale Funnel)。 |
| `CORNUS_TUNNEL_AUTHTOKEN` | — | — | クライアントが資格情報を省略した場合に使われる、選択したトンネルバックエンドのサーバー側既定資格情報。同じ変数名は、クライアント自身の環境で設定した場合、クライアントの `cornus tunnel --authtoken` フラグの値にもなります — 同じ名前で 2 つの異なるプロセスに使われますが、値の種類は同じです。 |
| `CORNUS_TUNNEL_CLOUDFLARED_BIN` | — | `cloudflared` on パス | `cloudflared` binary へのパス。 |
| `CORNUS_TUNNEL_TAILSCALE_BIN` | — | `tailscale` on パス | `tailscale` binary へのパス。 |
| `CORNUS_TUNNEL_SSH_ADDR` | — | — | SSH トンネルサーバーアドレス。 |
| `CORNUS_TUNNEL_SSH_USER` | — | — | SSH トンネル user。 |
| `CORNUS_TUNNEL_SSH_BIND` | — | — | SSH リバーストンネルのリモートバインドアドレス。[イングレストンネル](/ja/cli/ingress-tunnel) は、ポートを維持したままホスト部分を公開対象のイングレスホスト名へ置き換えることがあります。これにより sish 形式の中継が宣言済みホスト名を割り当てます。 |
| `CORNUS_TUNNEL_SSH_URL_TEMPLATE` | — | — | SSH トンネルから導出するパブリック URL の template。 |
| `CORNUS_TUNNEL_SSH_URL_FROM_SESSION` | — | off | SSH セッション出力からパブリック URL を導出します。 |
| `CORNUS_TUNNEL_SSH_HOSTKEY` | — | — | expected SSH ホストキー。 |
| `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` | — | — | SSH ホスト verification 用 `known_hosts` ファイルへのパス。 |
| `CORNUS_TUNNEL_SSH_INSECURE` | — | off | SSH host-key verification をスキップします (testing のみ)。 |

## Hub (ワークロード間オーバーレイ)

[ワークロード間 hub](/ja/guides/hub) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_HUB_STORE` | — | メモリ内 | Hub catalog ストア。`kube` は Kubernetes-backed ストアを使います。 |
| `CORNUS_HUB_REDIS` | — | — | 分散型ハブストア用の Redis URL (レプリカ間カタログを有効化)。 |
| `CORNUS_HUB_FORWARD_URL` | — | — | レプリカが hub 中継トラフィックを転送する URL。 |
| `CORNUS_HUB_FORWARD_CA` | — | — | hub 転送エンドポイントを検証する PEM CA bundle。 |
| `CORNUS_HUB_POLICY` | — | — | どの ID がどの hub サービスに到達できるかを管理するポリシー。 |
| `CORNUS_HUB_REGISTER_POLICY` | — | — | どの ID が hub サービスを登録 (エクスポート) できるかを管理するポリシー。 |

クライアント認証が有効な場合、どちらかの分散型ストアを選択すると、レプリカ間転送資格情報も自動的に有効になります。各レプリカは ECDSA P-256 秘密鍵を `$CORNUS_DATA/peer.key` に保持し、既存のハートビートまたは Lease の有効期間に従って公開鍵だけを発行し、3 つの `/.cornus/v1/*/forward` エンドポイントへ 5 分間有効な `peer` スコープの ES256 JWT を送信します。追加の環境変数は不要です。`CORNUS_AUTH_TOKEN` が設定されている場合は、バージョンが混在するローリング更新のために、従来の共有トークンが絶対的な優先順位を維持します。

## オブザーバビリティ

オブザーバビリティ model は [アーキテクチャ overview](/ja/architecture/) を参照してください。

| 変数 | フラグ | 既定 | Meaning |
| --- | --- | --- | --- |
| `CORNUS_OTEL` | `--otel` | off | 標準 `OTEL_*` env による OpenTelemetry (trace/metric/log) を有効化します。`OTEL_*` exporter/endpoint env var が設定されている場合も暗黙に有効化されます。 |
| `CORNUS_METRICS_PROMETHEUS` | — | off | Prometheus metrics エンドポイントを公開します (OpenTelemetry が有効な場合のみ有効)。 |
| `CORNUS_OBS` | `--[no-]obs` | on | 組み込みオブザーバビリティストアを有効にし、デプロイ済みワークロードのログを記録して、その OTLP トレースとメトリクスをローカルデータベースへ受信します。cornus *自体*を計装する `CORNUS_OTEL` とは別の機能です。既定値はビルドによって異なります。リリース済みのすべてのバイナリと公開イメージ (いずれもストアを同梱) では on、`-tags "imbh sable_extern_lib"` なしで自分でビルドしたバイナリでは off です。無効にするには `--no-obs` を使います。 |
| `CORNUS_OBS_DIR` | `--obs-dir` | `<data-dir>/observability` | オブザーバビリティデータベースを保持するディレクトリ。相対パスはデータディレクトリを基準にします。 |
| `CORNUS_OBS_RETENTION` | `--obs-retention` | `168h` | これより古い記録済みテレメトリを破棄します (`0` = サイズ上限が適用されるまで保持)。日単位に切り上げられます。 |
| `CORNUS_OBS_MAX_BYTES` | `--obs-max-bytes` | `536870912` | ストアのディスク上のサイズ上限 (バイト単位、`0` = 無制限)。 |
| `CORNUS_OBS_RECORD_LOGS` | `--obs-record-logs` | on | 管理対象ワークロードのすべての stdout/stderr をストアに記録します。ワークロードごとに 1 本の follow ストリームを使います。無効にするには `--no-obs-record-logs` を使います。 |
| `CORNUS_OBS_RECORD_METRICS` | `--obs-record-metrics` | on | 管理対象ワークロードごとに CPU、メモリ、ネットワーク、ディスクの使用量を定期的にサンプリングして記録し、サーバー自体の使用量も併せて記録します。ログ記録とは異なり、これには `CORNUS_OBS` が**不要**です。`CORNUS_OBS_EXPORT_ENDPOINT` だけを設定して、保存せず転送する場合にも機能します。無効にするには `--no-obs-record-metrics` を使います。 |
| `CORNUS_OBS_METRICS_INTERVAL` | `--obs-metrics-interval` | `15s` | 各ワークロードレプリカをサンプリングし、サーバー自体のメトリクスを収集する間隔。短くすると解像度が上がりますが、保存されるデータポイント数とバックエンド呼び出し回数は比例して増えます。 |
| `CORNUS_OBS_EXPORT_ENDPOINT` | `--obs-export-endpoint` | — | 受信したワークロードテレメトリを、保存に加えて上流の OTLP/HTTP バックエンドへ転送します。`CORNUS_OBS` とは独立しています。ストアがある場合はコピーを保持して転送し、ない場合は純粋なテレメトリゲートウェイとして動作します (`imbh` ビルドは不要)。 |
| `CORNUS_OBS_EXPORT_HEADERS` | `--obs-export-header` | — | 転送する各エクスポートに追加する `KEY=VALUE` ヘッダー (例: 上流の認証トークン)。フラグでは繰り返し指定できます。 |
| `CORNUS_OBS_EXPORT_INSECURE` | `--obs-export-insecure` | off | 再エクスポート先の上流に対する TLS 検証をスキップします。 |

同じ `CORNUS_OTEL` / `OTEL_*` gate は **クライアント CLI** の tracing も有効化します。`cornus` を実行する環境に設定すると、各 invocation が root span を emit し、W3C `traceparent` をサーバー (さらに caretaker) へ伝搬します。そのため `cornus deploy` / `cornus build` / `cornus compose up` は isolated サーバー span ではなく、1 つの end-to-end トレースとして見えます。

## クライアント側変数 (参考)

これらはサーバーではなく CLI が読みますが、同じ `CORNUS_*` 名前空間にあります。[接続設定](/ja/reference/connection-config) と [リモートクラスターで作業する](/ja/guides/remote-clusters) を参照してください。

| 変数 | 既定 | Meaning |
| --- | --- | --- |
| `CORNUS_SERVER` / `CORNUS_HOST` | selected プロファイル, then `http://localhost:5000` | クライアントコマンド用リモート cornus サーバー URL。 |
| `CORNUS_TOKEN` | — | クライアント要求用 bearer トークン (プロファイルの `token` を上書き)。 |
| `CORNUS_TOKEN_CACHE` | `auto` | CLI が短命の資格情報 (発行された SSH 鍵セッション、交換されたトークン) を保持する場所: `auto` (OS のキーリング。使えない場合はファイルにフォールバック)、`keyring`、`file`、`none` のいずれか。`none` は起動のたびに新しく発行します。保存された資格情報が許容できない環境ではこれを設定してください。ファイルバックエンドは `$XDG_RUNTIME_DIR/cornus/tokens` (0700 のディレクトリ内に 0600) に置かれ、tmpfs 上にあってログアウト時に消去されます。 |
| `CORNUS_CONFIG` | platform 設定パス | クライアント [接続設定](/ja/reference/connection-config) ファイルへのパス。 |
| `CORNUS_CONTEXT` | 設定 `current-context` | 使用する接続プロファイル。 |
| `CORNUS_OUTPUT` | `auto` | 出力 rendering モード (`auto`、`plain`、`fancy`、`json`)。[出力 modes](/ja/guides/output-modes) を参照。 |
| `CORNUS_CONDUIT` | プロファイル / `port-forward` | セッション conduit モード (`port-forward` または `socks5`)。 |
| `CORNUS_VIA_SERVER` | プロファイル / 直接 | ワークロード streaming をサーバープロキシ経由にします。 |
| `CORNUS_BUILDER` | — | delegated ビルド用リモートビルドエンドポイント。 |
| `CORNUS_REGISTRY` | server-advertised ホスト | レジストリ part を持たないタグ用レジストリホスト (リモートビルド)。 |
| `CORNUS_GH_BIN` | PATH 上の `gh` | `github-cli` [資格情報](/ja/guides/credentials)ソースが実行する GitHub CLI のパス。トークンが発行されるのはデプロイセッションを保持しているマシンなので、この変数もそこで読まれます。スペックに明示的な `config.command` があればそちらが優先されます。 |
