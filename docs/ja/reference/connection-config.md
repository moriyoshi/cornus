# 接続設定リファレンス

**接続設定** は、リモート cornus サーバーへの到達方法を記述する CLI 側の kubeconfig 風ファイルです。名前付き **コンテキスト** の集合であり、各コンテキストはエンドポイント、資格情報、TLS material、任意のクラスター内ポート転送対象を持ちます。これは developer の machine 上にあり、**サーバーから読まれることはありません** (サーバー側には別の data-directory 設定があります)。

通常、このファイルは手で編集するのではなく [`cornus config`](/ja/cli/config) で管理しますが、format をここに document します。canonical な正本は [`pkg/clientconfig/clientconfig.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/clientconfig/clientconfig.go) です。

## ファイル location

既定パスは platform user 設定ディレクトリの下の `cornus/config.yaml` です。

- Linux/BSD: `~/.config/cornus/config.yaml`
- macOS: `~/Library/Application Support/cornus/config.yaml`
- Windows: `%AppData%\cornus\config.yaml`

明示的に設定された `$XDG_CONFIG_HOME` は **すべての** OS で尊重されます (XDG に統一している user 向けの opt-in)。その場合、ファイルは `$XDG_CONFIG_HOME/cornus/config.yaml` になります。グローバル `--config-file` フラグと `CORNUS_CONFIG` 環境変数はパス全体を上書きします。

このファイルは bearer トークンとキーパスを保持するため、`0700` ディレクトリの下にモード `0600` で書き込まれます。ファイルが存在しないことはエラーではありません。CLI は空の設定と同じように扱います。

## Sample 設定

```yaml
current-context: staging
contexts:
  local:
    server: http://127.0.0.1:5000

  staging:
    server: https://cornus.staging.example.com
    token: eyJhbGciOi...
    tls:
      ca-cert: /etc/cornus/staging-ca.pem
    conduit:
      mode: socks5
      socks5:
        listen: 127.0.0.1:1080
        service-host-suffix: .cornus.internal

  prod-cluster:
    # No static server URL: dial the in-cluster Service via port-forward.
    port-forward:
      kube-context: prod
      namespace: cornus
      service: cornus
      remote-port: 5000
    kube-auth:
      audience: cornus
      expiration-seconds: 3600
    registry-host: registry.prod.example.com:5000
```

## `File`

top-level document です。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `current-context` | string | — | `--context` フラグが指定されない場合に使われるコンテキスト。空のは「コンテキスト未選択」を意味し、CLI はコマンドごとのフラグと環境変数に頼ります。 |
| `contexts` | map[string][Context](#context) | — | 名前付き接続プロファイル。name をキーにします。 |

## `Context`

1 つの名前付きリモートエンドポイントと、それへ到達するための資格情報 / 転送経路 setting です。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `server` | string | — | cornus サーバー base URL (例: `https://cornus.example.com` または `http://127.0.0.1:5000`)。`port-forward` が設定され、`server` が空の場合、CLI はクラスター内サービスへ転送し、そのローカル end に接続します。 |
| `registry-host` | string | derived from the サーバー | ビルドイメージのタグとデプロイプル ref に入る `host[:port]` を上書きします。空の (通常) なら導出します。CLI はサーバー (`GET /.cornus/v1/info`) に問い合わせ、フォールバックとして `server` エンドポイントのホストを使います。サーバーが introspect できない topology でのみ設定してください。 |
| `token` | string | `CORNUS_TOKEN` env | `Authorization: Bearer` として送る bearer トークン / JWT。空の場合は `CORNUS_TOKEN` 環境変数にフォールバックします。 |
| `tls` | [TLS](#tls) | system defaults | HTTPS エンドポイント用の任意の custom-CA / mTLS / insecure setting。 |
| `port-forward` | [PortForward](#portforward) | — | 設定されている場合、接続前に CLI がポート転送するクラスター内サービス。 |
| `kube-auth` | [KubeAuth](#kubeauth) | — | 設定されている場合、静的 `token` の代わりにクラスターから bearer トークンを導出します (Kubernetes TokenRequest API による短命 ServiceAccount トークン)。`token` より優先されますが、明示的な `CORNUS_TOKEN` 上書きには譲ります。 |
| `via-server` | bool (nullable) | unset (直接) | ワークロード streaming operation (compose ログ、ポート転送) を、developer の kubeconfig でワークロード pod へ直接到達する代わりに cornus サーバープロキシ経由に強制します。クラスタープロファイルでのみ意味があります。`CORNUS_VIA_SERVER` env var と `--via-server` フラグより低い、最下位 precedence レイヤーです。transport-only であり、`kube-auth` トークン発行は無効化しません。 |
| `conduit` | [Conduit](#conduit) | ポート転送 | クライアントセッションがデプロイメントのポートを呼び出し元に公開する方法。`CORNUS_CONDUIT` env var と `--conduit` フラグより低い、最下位 precedence レイヤーです。[ネットワークと conduit](/ja/guides/networking) を参照してください。 |

## `Conduit`

コンテキストのセッション conduit preference です。モードと、SOCKS5 の場合はプロキシ setting を持ちます。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `mode` | string | `port-forward` | `port-forward` (ポートごとの自動転送、Compose-like) または `socks5` (単一のクライアント側 SOCKS5 スプリットトンネルプロキシ)。 |
| `socks5` | [Socks5](#socks5) | — | SOCKS5 プロキシを調整します。`mode` が `socks5` の場合だけ参照されます。 |

## `Socks5`

SOCKS5 スプリットトンネルプロキシを設定します。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `listen` | string | `127.0.0.1:1080` | プロキシがバインドするローカルアドレス。 |
| `service-host-suffix` | string | `.cornus.internal` | 日常的な既定 resolution 規則を作ります。この接尾辞を持つ CONNECT ホストはサービス name に削られてトンネルされ、それ以外は直接エグレスします。`resolve` が設定されている場合は無視されます。 |
| `resolve` | [][ResolveRule](#resolverule) | — | 接尾辞既定全体を置き換える advanced で ordered な resolution 規則 list。最初に match した規則が勝ちます。 |
| `bare-service-names` | bool (nullable) | 有効 | 稼働中サービス名を表す素の single-label ホスト (例: `web`、`web.cornus.internal` に加えて) を内向きに経路するかどうか。サービス name が直接到達する real single-label ホストを shadow してしまう場合は `false` にします。 |

## `ResolveRule`

SOCKS5 resolution 規則 1 つです。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `pattern` | string | — | `host:port` CONNECT subject に対して test される regexp。 |
| `replace` | string | — | `service:port` を生成する template (sed-style の `\1` backreference を受け付けます)。 |

## `TLS`

HTTPS エンドポイント用のクライアント側 TLS material です。どれも設定されていない場合、`Config()` は system 既定を返します。`client-cert` と `client-key` は一緒に設定する必要があります。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `ca-cert` | string | system trust ストア | サーバー証明書を検証する PEM CA bundle へのパス。サーバーの CA が system trust ストアにない場合に使います。 |
| `insecure-skip-verify` | bool | `false` | サーバー証明書 verification を無効化します。testing のみ。 |
| `client-cert` | string | — | mTLS 用 PEM クライアント証明書へのパス。 |
| `client-key` | string | — | mTLS 用の対応する PEM クライアントキーへのパス。 |

mTLS と bearer 認証のサーバー側については [セキュリティと認証](/ja/guides/security) を参照してください。

## `PortForward`

接続前に転送するクラスター内サービスです (CLI の service-forwarder が消費します)。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `kube-context` | string | current kube コンテキスト | 使用する kubeconfig コンテキスト。 |
| `namespace` | string | — | サービスの名前空間。 |
| `service` | string | — | 転送先サービス name。 |
| `remote-port` | int | — | サービスポート。CLI は ready backing pod とその対象ポートに解決します。 |

## `KubeAuth`

cornus bearer 資格情報として発行する cluster-issued ServiceAccount トークンです。

| フィールド | 型 | 既定 | 説明 |
| --- | --- | --- | --- |
| `kube-context` | string | the `port-forward` block's 値 | 発行先 kubeconfig コンテキスト。 |
| `namespace` | string | the `port-forward` block's 値 | ServiceAccount の名前空間。 |
| `service-account` | string | — | トークンを発行する ServiceAccount。 |
| `audience` | string | — | トークン audience。サーバーの `CORNUS_JWT_AUDIENCE` と一致する必要があります。 |
| `expiration-seconds` | int64 | クラスター既定 | 要求するトークン lifetime。 |

## プロジェクトコンテキスト上書き

プロジェクトには、bare `Context` 文書である `cornus-context.json`、`cornus-context.yaml`、`cornus-context.yml`、または `cornus-context.toml` を置けます。Cornus は作業ディレクトリから上方向に検索し、最も近いファイルを使い、リポジトリルートまたはホームディレクトリで停止します。そのフィールドは選択した保存済みコンテキストに重ねられます。明示的なコマンドフラグと環境変数が引き続き優先されます。保存済みコンテキストが選択されていない場合にも接続を提供できます。

```yaml
server: https://cornus.staging.example.com
via-server: true
conduit:
  mode: socks5
```

明示的なファイルには `--context-file PATH` または `CORNUS_CONTEXT_FILE=PATH` を使います。明示的に指定したファイルがない場合はエラーです。`--no-context-file` は検出を無効にし、`--context-file` と併用できません。

### 信頼境界

自動検出したファイルは、信頼済み資格情報ストアではなく作業ツリー入力です。既定では `via-server` だけを反映し、endpoint、token、TLS、registry、port-forward、kube-auth、SSH-tunnel、conduit の設定は無視します。Unix では、別ユーザーが所有するファイル、または world-writable かつ non-sticky なディレクトリ内のファイルも無視します。

`--trust-context-file` / `CORNUS_TRUST_CONTEXT_FILE=1` は信頼できる作業ツリーでのみ使ってください。明示的に名前を指定した `--context-file` も信頼されます。endpoint を変更する上書きには独自の `token` または `kube-auth` が必要で、それがない場合は選択済みコンテキストの資格情報を破棄します。Cornus はプロジェクト上書きをスキップまたはフィールドを除去すると警告します。

## 関連ページ

- [`cornus config`](/ja/cli/config) - コンテキストの作成、選択、編集。
- [ネットワークと conduit](/ja/guides/networking) - conduit モードとポート転送。
- [リモートクラスターで作業する](/ja/guides/remote-clusters) - プロファイルからリモートサーバーを操作する方法。
- [セキュリティと認証](/ja/guides/security) - bearer トークン、mTLS、クラスターが発行する ID。
