# 資格情報

Cornus は、実行中のワークロードにクラウド資格情報、LLM API キーなどのシークレットを渡せます。しかも **シークレットがイメージ、デプロイスペック、Pod 仕様に入ることはありません**。資格情報は呼び出し元のマシン上でローカルの資格情報から発行され、稼働中の deploy-attach 接続でサーバーを経由して中継され、Pod ごとの caretaker サイドカーによってコンテナへ渡されます。必要に応じて取得され、TTL 付きでキャッシュされ、有効期限が近づくと更新されます。関連機能として、ワークロードの外向き通信を呼び出し元経由にする[クライアント側エグレス](/ja/guides/egress)があります。

## 仕組み

これはデプロイスペックの `credentials:` ブロックとして宣言され、フォアグラウンドの `cornus deploy --server` セッション上で **Kubernetes** バックエンドにより実現されます。クライアントは、ワークロードの存続中に行われる取得要求へ応答するため接続を維持するので、`--detach` とホストバックエンドはこれを拒否します。`sources:` の各項目は、シークレットを生成するクライアント側**バックエンド**と、コンテナから利用できるようにする 1 つ以上の**配送方法**を指定します。

サーバーに送られるのはバックエンド名と非シークレットの `config` だけです。シークレットは取得時にバックエンドによって生成されます。

### ソースバックエンド

各バックエンドは呼び出し元自身の環境から資格情報を生成します。

| `backend` | 生成元 | 備考 |
| --- | --- | --- |
| `static` | リテラルの `config` 値 (またはファイル) | |
| `exec` | `config.command` の標準出力 | JSON、または `config.key` 下の単一 `raw` 値 |
| `env` | クライアント環境変数 (`config.var`) | 例: `ANTHROPIC_API_KEY` |
| `aws-sts` | AWS 資格情報チェーンを使う STS 経由の短命 AWS 資格情報 | `credaws` タグ付きバイナリが必要。モードは `auto` / `assume-role` / `session-token` / `passthrough` |
| `anthropic` / `claude-code` / `codex` | ローカルの LLM ログイン情報 | 短命トークンを有効期限が近づくと再読み込み |

### 配送方法

`deliver[].kind` の既定は `endpoint` です。

- **`endpoint`** - caretaker がループバック HTTP エンドポイントから資格情報を提供します。`provider: generic` (既定) はネイティブ契約 (`GET /credentials/<name>` が `{"values":{...},"expiration":"..."}` を返す) を提供し、`CORNUS_CREDENTIALS_URL` / `CORNUS_CREDENTIAL_<NAME>_URL` でアプリケーションに通知します。`provider: aws-imds` は変更していない AWS SDK が期待する形式で資格情報を描画します。下の[AWS STS から資格情報を取得する](#aws-sts-から資格情報を取得する)を参照してください。
- **`file`** - 共有ボリューム内の `path:` に実体化します。`format:` は `json` (既定)、`env` (`KEY=VALUE` 行)、`raw` (単一値)、または `aws-credentials` (ini プロファイル) です。モード `0600` で書き込まれます。
- **`env`** - アプリケーションコンテナに `envVar:` を注入します。値はデプロイ時に一度取得され、`secretKeyRef` 経由で参照される Kubernetes シークレットに保存されます (Pod 仕様のリテラルにはなりません)。ただし静的で更新されず etcd に残るため、短命または実体化したくないシークレットには `endpoint` / `file` を推奨します。

### 信頼性

シークレットは稼働中のセッションで取得のたびに返され、仕様や wire 制御フレームには決して入りません。ワークロードが取得できるのは、自身のデプロイセッションが宣言した資格情報名 **だけ**です。セッション ID は推測不能な能力トークンであり、サーバー中継と caretaker の両方で検査されます。認証プロキシは本物の資格情報を注入する前にクライアント提供の認証情報を除去するため、ワークロードは生のシークレットを読むことも偽装することもできません。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## イメージに焼き込まず資格情報をワークロードへ仲介する

`credentials:` ブロックを宣言します。シークレットは自分のマシンで発行され、caretaker が配送するため、イメージ、仕様、Pod 仕様には一切入りません。

```yaml
name: app
image: localhost:5000/app:v1
credentials:
  sources:
    - name: db
      backend: static                              # produce the secret on the client
      config: { username: app, password: s3cret }  # non-secret config for other backends
      deliver:
        - { kind: endpoint, provider: generic }        # GET $CORNUS_CREDENTIALS_URL -> JSON
        - { kind: file, path: /creds/db.json, format: json }
```

- **kubernetes** バックエンドで、フォアグラウンドの `cornus deploy --server` セッションを通じて実現されます (ワークロードの存続期間中クライアントが取得要求へ応答するため、`--detach` は拒否します)。
- `deliver[].kind` は `endpoint` (既定)、`file`、`env` のいずれかです。ワークロードが取得できるのは自身のセッションが宣言した資格情報名だけです。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## LLM API をプロキシする、または API キーをワークロードへ注入する

`anthropic-proxy` と `openai-proxy` のエンドポイントプロバイダーは、資格情報を提供するだけでなくさらに一歩進みます。caretaker がベンダー API へのループバックリバースプロキシを実行し、**認証ヘッダーを自ら注入** します。そのためワークロードは自前のキーなしで LLM を呼び出せます。アプリケーションには `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` を設定し、クライアントが送った認証情報をすべて除去して、要求ごとに本物の資格情報を追加します。つまりコーディングエージェントのワークロードは、シークレットがコンテナに入ることなく **自分自身の** Claude Code / Codex のログイン情報を利用できます。

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional: Azure OpenAI, an on-prem gateway, a mock
```

- `upstream` はベンダーの既定値 (`https://api.anthropic.com` / `https://api.openai.com`) の代わりに、任意の Anthropic 互換または OpenAI 互換ゲートウェイをプロキシ先にします。
- 単純な環境変数を注入するには、`backend: env` を `config.var` とともに使い、`env` 種別で配送します (静的な資格情報は Kubernetes シークレットに保存されます。短期間有効なシークレットには `endpoint` / `file` を推奨します)。

### API キーと OAuth トークン

プロキシは両方の資格情報形式を透過的に扱うため、プレーンな API キー **または** OAuth ログイントークンのどちらでも、ワークロードを変更せずに動作します。

- **API キー** はベンダーが通常使うキーヘッダーで送られます (Anthropic では `x-api-key`)。
- **OAuth トークン**、たとえば `claude` / `ant auth login` でログインして得る `sk-ant-oat...` トークンは、Anthropic API が OAuth ベアラートークンに要求する `anthropic-beta: oauth-2025-04-20` ヘッダーとともに `Authorization: Bearer <token>` として送られます。プロキシは資格情報値を `oauth_token` (OAuth を強制)、`api_key` (API キーを強制)、それ以外は `value` / `token` の順で選びます。

`anthropic` / `claude-code` / `codex` ソースバックエンドはローカルのログインストアを読み、有効期限が近づくと **短命 OAuth アクセストークンを更新** します (codex は ChatGPT のログイン情報にある `tokens.access_token` を読み、API キーにフォールバックします)。そのため長時間稼働するエージェントは再認証なしに動き続け、トークンはそれでもコンテナには入りません。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## AWS STS から資格情報を取得する

自分の AWS 資格情報チェーンから短期間有効な AWS 資格情報を発行し、SDK が期待する形式で渡します。

```yaml
credentials:
  sources:
    - name: aws
      backend: aws-sts
      config: { role_arn: arn:aws:iam::123456789012:role/app, region: us-east-1 }
      deliver:
        - { kind: endpoint, provider: aws-imds, wellKnown: true }
        - { kind: file, path: /root/.aws/credentials, format: aws-credentials }
```

- `aws-sts` は STS を介して AWS 資格情報チェーンを使います。`credaws` タグ付きバイナリが必要で、`auto` / `assume-role` / `session-token` / `passthrough` モードをサポートします。

`aws-imds` エンドポイントプロバイダーは、ブローカーされた資格情報を AWS SDK がすでに探す形式で提供します。そのため **変更していない** SDK がコードやアプリケーションを変更せずに取得できます。アダプターは AWS SDK への依存を持たない純粋な HTTP 実装で、1 つのエンドポイントから 2 種類の形式で応答します。

- **ECS コンテナ資格情報** - `GET /creds` は `{AccessKeyId, SecretAccessKey, Token, Expiration}` を返します。
- **EC2 IMDSv2** - `PUT /latest/api/token` の後、`GET /latest/meta-data/iam/security-credentials/<role>` を呼び出します (一覧は単一の合成ロール `cornus` を通知します)。IMDSv1 クライアントはトークン取得を単にスキップします。

SDK がそこへ到達する方法は `wellKnown` によって異なります。

| `wellKnown` | バインド先 | SDK の見つけ方 | 必要なもの |
| --- | --- | --- | --- |
| `false` (既定) | ループバック | Cornus が `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://<loopback>/creds` を注入します。これは AWS SDK が尊重する標準の ECS 資格情報用環境変数です。 | 追加なし |
| `true` | Pod netns 内のリンクローカル `169.254.169.254:80` | SDK 組み込みの IMDSv2 パス。**環境変数は不要**で、本物の EC2 インスタンスと同じです。 | caretaker に `NET_ADMIN` |

これは配送用の *アダプター* であり、汎用メタデータサービスを運用するものではありません。ワークロードのセッションに対する、ブローカーされた 1 つの資格情報だけを提供します。同じ仕組みに GCP / Azure のメタデータアダプターを組み込めます。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)
