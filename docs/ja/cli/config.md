# cornus config

リモート Cornus サーバーへ到達するために使うクライアント側接続プロファイル (コンテキスト) を管理します。形式は `kubectl config` に対応しています。

## Synopsis

```sh
cornus config <subcommand> [flags]
```

## 説明

`cornus config` は Cornus クライアント設定ファイルを読み書きします。このファイルには、1 つ以上の名前付きコンテキスト (接続プロファイル) と現在のコンテキストへのポインターが保存されます。ファイルはプラットフォームのユーザー設定ディレクトリ、またはグローバル `--config-file` フラグ / `CORNUS_CONFIG` で指定されたパスに置かれます。

各コンテキストはサーバーへの到達方法を記述します。ベース URL、ベアラートークンまたは ServiceAccount から発行した認証情報、TLS 情報、クラスター内サービスへの任意の自動ポート転送、直接接続とプロキシを切り替える `via-server`、そしてセッションコンジット (ポート転送または SOCKS5) です。完全なスキーマは [接続設定](/ja/reference/connection-config) に文書化されています。

### クライアント設定ファイル format

ファイルは YAML で、name をキーにする `contexts:` map と `current-context:` フィールドを持ちます。例:

```yaml
current-context: prod
contexts:
  prod:
    server: https://cornus.example.com:5000
    token: eyJhbGci...
  staging:
    namespace: cornus-system
```

Bearer トークンは、`--show-tokens` (または `--export`) が指定されない限り `view` によって redact されます。すべてのフィールドは [接続設定](/ja/reference/connection-config) を参照してください。

## cornus config get-contexts

設定済み接続プロファイルを table として一覧します (current コンテキストには `*` が付きます)。

```sh
cornus config get-contexts
```

## cornus config current-context

current (既定) コンテキスト name を出力します。設定されていない場合はエラーになります。

```sh
cornus config current-context
```

## cornus config use-context

current (既定) コンテキストを設定します。

```sh
cornus config use-context <name>
```

## cornus config set-context

コンテキストを作成または更新します。

```sh
cornus config set-context [flags] <name>
```

既定では、`set-context` は同じ name の既存コンテキストを*置き換えます*。結果はこの invocation が指定した内容そのものになります。layering order は `--from-file` (base)、次に個別フラグ、最後に `--from-file-override` (top) です。既存コンテキストに対して指定した設定をレイヤーし、未設定フィールドをそのまま残す edit-in-place モードにするには、`--merge` を渡してください。

設定にまだコンテキストがなく、terminal が対話的の場合、新しく作成されたコンテキストを既定 (current) コンテキストにするか提案されます。`--insecure-skip-verify` は設定を有効化する方向にしか働きません。

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--server` | — | — | Cornus サーバー base URL (`http(s)://host:port`)。 |
| `--token` | — | — | `Authorization: Bearer` として送る Bearer トークン / JWT。 |
| `--tls-ca-cert` | — | — | サーバー証明書を検証する PEM CA bundle。 |
| `--tls-client-cert` | — | — | mTLS 用 PEM クライアント証明書 (`--tls-client-key` が必要)。 |
| `--tls-client-key` | — | — | mTLS 用 PEM クライアントキー (`--tls-client-cert` が必要)。 |
| `--insecure-skip-verify` | — | `false` | サーバー証明書 verification を無効化します (testing のみ)。 |
| `-n`, `--namespace` | — | — | cornus install の名前空間。`--pf-service` または `--no-detect` が設定されていない限り、サービスとポートを auto-detect します。 |
| `--no-detect` | — | `false` | クラスターに接続してサービスを detect せず、`--namespace` を保存します。 |
| `--pf-kube-context` | — | — | 自動ポート転送用 kubeconfig コンテキスト。 |
| `--pf-namespace` | — | — | ポート転送先クラスター内サービスの名前空間 (`--namespace` の alias)。 |
| `--pf-service` | — | — | ポート転送先クラスター内サービスの name (auto-detection をスキップ)。 |
| `--pf-remote-port` | — | — | ポート転送先サービスポート。 |
| `--kube-auth-service-account` | — | — | 静的 `--token` の代わりに、このクラスター ServiceAccount から TokenRequest API で bearer トークンを発行します。 |
| `--kube-auth-audience` | — | — | minted ServiceAccount トークンの audience。サーバーの `CORNUS_JWT_AUDIENCE` と一致する必要があります。 |
| `--kube-auth-namespace` | — | — | ServiceAccount の名前空間 (既定は `--pf-namespace`)。 |
| `--kube-auth-kube-context` | — | — | トークンを発行する kubeconfig コンテキスト (既定は `--pf-kube-context`)。 |
| `--kube-auth-expiration-seconds` | — | `3600` | 要求するトークン lifetime。秒単位 (0 = 既定 3600)。 |
| `--via-server` / `--no-via-server` | — | — | (クラスタープロファイルのみ) kubeconfig で pod へ直接到達する代わりに、ワークロードログ / ポート転送を cornus サーバープロキシ経由にします。`CORNUS_VIA_SERVER` またはコマンドの `--via-server` フラグにより run ごとに上書きされます。 |
| `--conduit-mode` | — | — | クライアントセッションがポートを公開する方法: `port-forward` (ポートごとのローカルリスナー、既定)、`socks5` (サービスに名前で到達する 1 つのスプリットトンネルプロキシ)、またはプロキシバインドアドレスと接尾辞も設定する `socks5://host:port[?suffix=SUFFIX]` URL。`CORNUS_CONDUIT` またはコマンドの `--conduit` フラグにより run ごとに上書きされます。 |
| `--socks5-service-host-suffix` | — | `.cornus.internal` | SOCKS5 `CONNECT` 対象が matching サービスへトンネルされるホスト接尾辞。他のホストは直接 conduit されます。 |
| `--socks5-resolve` | — | — | advanced SOCKS5 resolution 規則 `PATTERN=REPLACE` (繰り返し指定可能、ordered、first match wins)。接尾辞既定を置き換えます。 |
| `--from-file` | — | — | コンテキスト definition (素のコンテキスト object、JSON/YAML) を base レイヤーとして読み込み、個別フラグで上書きします。繰り返し指定可能。後のファイルが優先されます。 |
| `--from-file-override` | — | — | 個別フラグを上書きするコンテキスト definition を読み込みます。繰り返し指定可能。後のファイルが優先されます。 |
| `--merge` | — | `false` | 既存コンテキストを置き換えず、指定した設定を統合します。未設定フィールドは保存済みの値を保持します (edit-in-place)。 |

## cornus config delete-context

コンテキストを削除します。current-context pointer が削除対象コンテキストを指していた場合は clear されます。

```sh
cornus config delete-context <name>
```

## cornus config view

クライアント設定ファイルを出力します。既定では bearer トークンは redact されます。

```sh
cornus config view [flags]
```

`--export` は、`contexts:` wrapper なしの素のコンテキスト object として単一コンテキストを出力します。これは `set-context --from-file` に戻せる形式です。このモードでは再利用可能なエクスポートが目的なので、`--redact` を指定しない限りトークンは既定で含まれます。`--export` なしの場合、エクスポート対象コンテキストはグローバル `--context` フラグで選ばれ、なければ current コンテキストになります。

| フラグ | Env var | 既定 | 説明 |
| --- | --- | --- | --- |
| `--show-tokens` | — | `false` | bearer トークンを redact せずに出力します (whole-file view)。 |
| `--export` | — | `false` | 1 つのコンテキストだけを素のコンテキスト object として出力し、`set-context --from-file` に渡せる形にします。 |
| `--redact` | — | `false` | `--export` と併用し、bearer トークンを `REDACTED` に置き換えます (エクスポートは既定で real トークンを含みます)。 |
| `-o`, `--output-file` | — | stdout | stdout の代わりにこのファイルへ書き込みます (created `0600`)。 |

## Examples

サーバーに直接接続するコンテキストを作成し、それを current にします。

```sh
cornus config set-context prod --server https://cornus.example.com:5000 --token "$TOKEN"
cornus config use-context prod
```

クラスター内サービスを auto-detect し、ServiceAccount トークンを発行するクラスターコンテキストを作成します。

```sh
cornus config set-context staging \
  --namespace cornus-system \
  --kube-auth-service-account cornus-client \
  --kube-auth-audience cornus
```

既存コンテキストを in-place で編集します (未設定フィールドは保持)。

```sh
cornus config set-context prod --merge --conduit-mode socks5
```

別の場所で再利用するため、1 つのコンテキストをトークン付きでエクスポートします。

```sh
cornus config view --export --context prod -o prod-context.yaml
```
