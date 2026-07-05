# 資格情報

Cornus は、実行中のワークロードにクラウド資格情報、LLM API キーなどのシークレットを渡せます。しかも **シークレットがイメージ、デプロイスペック、Pod 仕様に入ることはありません**。資格情報は呼び出し元のマシン上でローカルの資格情報から発行され、稼働中の deploy-attach 接続でサーバーを経由して中継されます。

コンテナへの届き方は配送方法によって異なります。Kubernetes では `file` と `endpoint` の配送を Pod ごとの caretaker サイドカーが提供し、必要に応じて取得され、TTL 付きでキャッシュされ、有効期限が近づくと更新されます。ホストバックエンドではサーバー自身が 3 種すべてを行うため、caretaker はどこにも必要ありません。`env` の値はデプロイ時に解決し、`file` は実体化してバインドし、`endpoint` はワークロード自身のネットワーク名前空間の中にリスナーをバインドします。関連機能として、ワークロードの外向き通信を呼び出し元経由にする[クライアント側エグレス](/ja/guides/egress)があります。

## 仕組み

これはデプロイスペックの `credentials:` ブロック、または Compose サービスの `x-cornus-credentials:` として宣言されます (後者もフィールドは同じです。下記の [Compose ファイルから](#compose-ファイルから) を参照してください) 。どの配送方法も、ワークロードの存続中クライアントが維持するセッションを必要とします。したがって `cornus deploy --detach` はすべてのバックエンドでこれを拒否します。`sources:` の各項目は、シークレットを生成するクライアント側**バックエンド**と、コンテナから利用できるようにする 1 つ以上の**配送方法**を指定します。

利用できる配送方法はバックエンドによって異なります。

| バックエンド | `env` | `file` | `endpoint` |
| --- | --- | --- | --- |
| `kubernetes` | 対応 (Secret と `secretKeyRef` 経由) | 対応 | 対応 |
| `dockerhost` / `podman` / `bare` | 対応 | 対応 | 対応 |
| `podman` (rootless) | 対応 | 対応 | 対応 |
| `containerd` | 対応 | 対応 | 対応 |
| `incus` | 対応 | 非対応 (下記参照) | 対応 |

ホストバックエンドでは **どの配送方法にも caretaker が不要** です。したがっていずれの場合も `CORNUS_ADVERTISE_URL` も `CORNUS_AGENT_IMAGE` も必要ありません。

- **`env`** の値はデプロイ時に一度解決され、コンテナ作成時に環境変数へ設定されます。
- **`file`** はサーバーが自身のデータディレクトリ配下へ描画し、ワークロードへ読み取り専用でバインドしたうえで、資格情報の TTL に合わせてシンボリックリンクを差し替えて更新します。これは Kubernetes と同じアトミック書き込みの形です。
- **`endpoint`** はサーバーが *ワークロード自身のネットワーク名前空間の中に* バインドしたリスナーで、セッションの存続期間中サーバーが提供します。これは Kubernetes の caretaker より弱いモデルではなく同じモデルです。ワークロードが `127.0.0.1` で到達できるのは名前空間を共有しているからであり、ホスト上の他のものからは一切到達できません。

知っておくべき点があります。

- **ファイルのバインドはファイルのディレクトリ全体を覆います。** `/creds/db.json` の資格情報は `/creds` をバインドするため、イメージがそこに置いていたものは隠れます。資格情報には専用のディレクトリを与えてください。Kubernetes も Secret ボリュームで同じ選択をしています。
- **`endpoint` はコンテナ起動後にバインドされます。** 起動直後にはリスナーがまだ立ち上がっておらず接続が拒否される短い時間帯があります。資格情報エンドポイントのクライアントは再試行するため、ここでは許容できますが、ファイルでは許容できません。`dockerhost` ではこの時間帯を避けられません (Docker はコンテナ起動時にネットワーク名前空間を作成するため) 。`containerd` と `bare` は名前空間を自前で固定するため、この時間帯はより短くなります。
- **リモートモードでは両方とも拒否されます。** `CORNUS_DOCKER_REMOTE=1` (および containerd や bare の相当設定) ではランタイムが別のマシンにある可能性があり、そこではサーバーのパスもプロセス ID も何も指しません。しかも Docker は失敗せず空のディレクトリを作成してしまうため、黙って空のまま届いたり提供されなかったりするのではなく拒否します。
- **ID を再マップするランタイムには、実際に読む側の ID でファイルを所有させます。** rootless の `podman` はユーザー名前空間でコンテナを動かすため、コンテナ側 uid で所有させたファイルはワークロードから見えない ID の所有物として届きます (`nobody` と表示され、モードに関係なく読めません) 。cornus はランタイムに ID マップを問い合わせ、それに合わせて所有者を設定するので、`user: "1000"` のワークロードも自分の資格情報を読めます。そうしたランタイムではデータディレクトリが辿れるようになりますが (`0711`。辿れるだけで一覧はできません) 、そこに置かれるシークレットは `0600` のままです。
- **`incus` は `file` を拒否します。理由は権限ではなくタイミングです。** incus はインスタンスの ID マップを **インスタンス自身** に記録しますが、資格情報ファイルはインスタンスが存在する前に書く必要があります。作成リクエストのディスクデバイスとしてワークロードに届くためです。デーモン自身は ID マップの基点を公開していないので、事前に問い合わせる先がありません。`incus` では `env` と `endpoint` が動作するので、そちらを使ってください。

::: warning ホストバックエンドには `env` 配送を隠す Secret がありません
Kubernetes では `env` の配送は Secret に実体化され `secretKeyRef` で参照されます。Pod 仕様のリテラルにはなりません。ホストバックエンドにはそうした間接参照がないため、値はコンテナの設定に入り、デーモンと通信できる者なら誰でも読み取れます (`docker inspect`) 。これは特定のバックエンドの実装ではなく配送 *種別* に固有の性質です。短命または価値の高いシークレットには、利用できる場所では `file` または `endpoint` を推奨します。
:::

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
| `github-cli` | ローカルの `gh auth login` | `gh auth token` を実行。GitHub Enterprise には `hostname`、アカウントの選択には `user` |

### 配送方法

`deliveries[].kind` の既定は `endpoint` です。

- **`endpoint`** - caretaker がループバック HTTP エンドポイントから資格情報を提供します。`provider: generic` (既定) はネイティブ契約 (`GET /credentials/<name>` が `{"values":{...},"expiration":"..."}` を返す) を提供し、`CORNUS_CREDENTIALS_URL` / `CORNUS_CREDENTIAL_<NAME>_URL` でアプリケーションに通知します。`provider: aws-imds` は変更していない AWS SDK が期待する形式で資格情報を描画します。下の[AWS STS から資格情報を取得する](#aws-sts-から資格情報を取得する)を参照してください。認証を注入するプロバイダー (`anthropic-proxy`、`openai-proxy`、`github-proxy`) はさらに一歩進んで資格情報を自ら保持するため、コンテナはそれを一切受け取りません。
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
      backend: static                              # クライアント上でシークレットを生成
      config: { username: app, password: s3cret }  # 他のバックエンド向けの非シークレット設定
      deliveries:
        - { kind: endpoint, provider: generic }        # GET $CORNUS_CREDENTIALS_URL -> JSON
        - { kind: file, path: /creds/db.json, format: json }
```

- フォアグラウンドの `cornus deploy --server` セッションが必要です (ワークロードの存続期間中クライアントが取得要求へ応答するため、`--detach` は拒否します)。
- `deliveries[].kind` は `endpoint` (既定)、`file`、`env` のいずれかです。ワークロードが取得できるのは自身のセッションが宣言した資格情報名だけです。
- 上記の `endpoint` の配送は現時点では **Kubernetes 専用** です。`file` と `env` はホストバックエンドでも動作します。[対応表](#仕組み)を参照してください。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## LLM API をプロキシする、または API キーをワークロードへ注入する

`anthropic-proxy` と `openai-proxy` のエンドポイントプロバイダーは、資格情報を提供するだけでなくさらに一歩進みます。caretaker がベンダー API へのループバックリバースプロキシを実行し、**認証ヘッダーを自ら注入** します。そのためワークロードは自前のキーなしで LLM を呼び出せます。アプリケーションには `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` を設定し、あわせて*プレースホルダー*の `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` も設定します (資格情報を実際に保持しているのはプロキシであるにもかかわらず、SDK や CLI はキーの環境変数が無いと起動を拒否するためです) 。そのうえでクライアントが送った認証情報をすべて除去して、要求ごとに本物の資格情報を追加します。つまりコーディングエージェントのワークロードは、シークレットがコンテナに入ることなく **自分自身の** Claude Code / Codex のログイン情報を利用できます。

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # または: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliveries:
        - kind: endpoint
          provider: anthropic-proxy         # ANTHROPIC_BASE_URL を設定し、ヘッダーを注入
          # upstream: https://my-gateway    # 任意: Azure OpenAI、オンプレミスゲートウェイ、モック
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
      deliveries:
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

## 自分の `gh` ログイン情報でワークロードに GitHub API アクセスを与える

`github-cli` ソースは自分のマシンで `gh auth token` を実行し、`github-proxy` エンドポイントプロバイダーはそのトークンをワークロードの GitHub **REST API** 呼び出しへ注入します。コンテナはトークンを一切保持せずに認証済みの呼び出しを行えます。

```yaml
credentials:
  sources:
    - name: gh
      backend: github-cli
      ttl: 1h                                # gh は有効期限を報告しません。下記参照
      deliveries:
        - kind: endpoint
          provider: github-proxy             # GITHUB_API_URL を設定し、ヘッダーを注入
          # upstream: https://ghe.corp/api/v3   # GitHub Enterprise Server
```

トークンは `gh` が保存している場所から読み取ります。多くのマシンでは OS のキーリングであり、`~/.config/gh/hosts.yml` を読むだけでは取得できない場合でもこの方法なら動作します。`gh` は `GH_TOKEN` / `GITHUB_TOKEN` (他ホストでは `GH_ENTERPRISE_TOKEN`) も尊重するため、同じスペックが CI でもそのまま動きます。設定キーは `hostname` (GitHub Enterprise) 、`user` (ログイン済みアカウントの選択) 、`command`、`timeout`、`key` です。

`gh` が `PATH` に無い場合や別の名前でインストールされている場合は、デプロイセッションを保持しているマシンで `CORNUS_GH_BIN` を設定してください。トークンが発行されるのはそのマシンであり、この変数もそこで読まれます。これは共有スペックを編集せずに 1 台のマシンへ適合させるためのものです。明示的な `config.command` はスペックを書いた人の意図的な選択 (たとえば迂回されては困るラッパー) であるため、こちらが優先されます。優先順位は `config.command`、次に `CORNUS_GH_BIN`、最後に `gh` です。

```sh
CORNUS_GH_BIN=/opt/homebrew/bin/gh cornus deploy --server -f app.yaml
```

`ttl:` は明示的に設定してください。`gh auth token` は有効期限を報告しないため、既定の 5 分ではレプリカごとに 5 分おきに `gh` が実行され、キーリングに触れる可能性があります。有効期限のないトークンには 1 時間で十分です。

### これは REST API であり、git ではありません

`git clone` と `git push` はこのプロキシを**通りません**。git over HTTPS は `github.com:443` へ直接向かうため影響を受けません。`gh` CLI 自体もこのプロキシでは動きません。`gh` はベース URL ではなくホスト名を受け取り、常に HTTPS で通信するため、平文のループバックサイドカーを指定できないからです。どちらの場合も、代わりにトークン自体を配送し、それがコンテナに入ることを受け入れてください。

```yaml
deliveries:
  - { kind: endpoint }                                   # GET $CORNUS_CREDENTIALS_URL -> JSON
  - { kind: file, path: /run/secrets/gh-token, format: raw }
```

### クライアントをプロキシに向ける

`GITHUB_API_URL` を自動的に読むのは `@actions/github` だけです。他のクライアントはいずれも 1 行の設定が必要です。

| クライアント | |
| --- | --- |
| Octokit (JS) | `new Octokit({ baseUrl: process.env.GITHUB_API_URL })` |
| PyGithub | `Github(base_url=os.environ["GITHUB_API_URL"])` |
| go-github | `c := github.NewClient(nil); c.BaseURL, _ = url.Parse(os.Getenv("GITHUB_API_URL") + "/")` |

go-github では `BaseURL` を直接、**末尾のスラッシュ付きで**設定してください。`WithEnterpriseURLs` は使ってはいけません。これは `api.github.com` に見えないホストすべてに `api/v3/` を付け足すため、ループバックプロキシのアドレスが `https://api.github.com/api/v3/...` になって 404 になります。

既定のアップストリームでは `GITHUB_GRAPHQL_URL` も `GITHUB_API_URL` とあわせて設定されます。GitHub Enterprise の `upstream` では意図的に**省略**されます。GHES は REST を `/api/v3` の下で、GraphQL を兄弟の `/api/graphql` の下で提供しており、単一のプロキシでは到達できないためです。誤った URL を通知すると、クライアントは資格情報なしで GHES へ向かってしまいます。

コンテナに `GITHUB_TOKEN` は設定されません。これは意図的です。プレースホルダーを置くと `gh`、git の資格情報ヘルパー、`api.github.com` を直接呼ぶスクリプトがそれを拾ってしまい、「資格情報が無い」という状態が原因から遠く離れた場所での不可解な `401` に変わるからです。クライアントがこの環境変数の存在を要求する場合は、スペックの `env:` に自分でダミー値を設定してください。プロキシはクライアントが送った値を除去します。

### 押さえるべき 2 点

**`hostname` と `upstream` を揃えてください。** ソースの `hostname` は自分のマシンで、配送の `upstream` はデプロイ経路で解決され、両者が一致しているかを検査する仕組みはありません。`hostname: ghe.corp` の `github-cli` ソースと既定の `upstream` を組み合わせると、有効な GitHub Enterprise の資格情報が `api.github.com` へ送られます。この 2 つは必ずセットで設定してください。

**スコープに注意してください。** `gh auth login` のトークンは通常 `repo` (アクセスできるすべてのプライベートリポジトリへの読み書き) 、`read:org`、多くの場合 `workflow` を含みます。Pod 内で動作するものはすべてループバックプロキシを通じて自分自身として振る舞え、それを狭める方法はありません。LLM のキーよりもはるかに影響範囲が広いということです。暴走したループは自分のレート制限も消費します。完全に信頼できるワークロード以外では、fine-grained PAT を発行して `static` / `env` / `exec` で配送してください。

対象外: `uploads.github.com` (リリースアセット) は別のホストです。レスポンス*ボディ*内の絶対 URL は書き換えられません (`Link` と `Location` ヘッダーは書き換えられるため、ページネーションとリダイレクトはプロキシ上に留まります) 。プライベート CA の背後にある GitHub Enterprise インスタンスでは、その CA を caretaker のイメージに含めるか `SSL_CERT_FILE` で渡す必要があります。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## Compose ファイルから

Compose サービスは同じブロックを `x-cornus-credentials:` として宣言します。したがってエージェント、そのデータベース、そのキャッシュといったスタック全体を `cornus compose up` 一発で立ち上げつつ、エージェントはあなた自身のログインに相乗りできます。

```yaml
services:
  agent:
    image: localhost:5000/agent:v1
    x-cornus-credentials:
      - name: claude
        backend: claude-code
        deliveries:
          - { kind: endpoint, provider: anthropic-proxy }
  db:
    image: postgres:16
```

- ブロックは上記の素のソースリストか、スペックのオブジェクト形式 (同じリストを持つ `sources:`) です。スペックのブロックをそのまま貼り付けられます。
- 配送フィールドは Compose の snake_case 綴り (`well_known`、`env_var`、`value_key`) を取ります。スペックの camelCase 綴りも動作します。どちらでもないキーは、黙って無視されるのではなくエラーになります。
- 宣言したサービスは、そのライフタイムにわたって deploy-attach セッションを保持します。`cornus compose up -d` ではプロジェクトのバックグラウンドエージェントが保持するため、`cornus deploy --detach` とは異なり、デタッチした compose の `up` でも資格情報を利用できます。 `up` の行はその理由 (`brokering credentials`) を示します。
- ホストバックエンドは資格情報の配送を実装していません。デプロイを拒否するか、警告してブロックを無視します。
- プロジェクトレベルの `x-cornus-credentials:` ブロックは、自分で宣言していないすべてのサービスの既定値になります。サービス側のブロックはそれを丸ごと上書きします。継承した各サービスはそれぞれ自分のセッションを保持します。

**関連項目:** [cornus compose](/ja/cli/compose)
