# クライアントエグレスルーティングでコンテナ内 AI エージェントを実行する

## シナリオ

チームが自律 AI エージェント (コーディングエージェント、データエージェント) をクラスター上のワークロードとして実行しているものの、LLM API (たとえば Anthropic API) への呼び出しは*あなたの*ネットワーク経由でしか外へ出せないとします。API は開発者のマシンにある企業プロキシ、VPN、SASE パスからは到達できますが、クラスターからは到達できません。さらに API キーは実行時にブローカー経由で渡し、イメージ、デプロイスペック、Pod 仕様へ絶対に焼き込んではいけません。Cornus は両方を同時に解決します。[クライアント側エグレス](/ja/topics/egress)がエージェントの外向き API 呼び出しをマシン経由に戻し、[資格情報ブローキング](/ja/topics/credentials)がラップトップ上にしか存在しないシークレットを実行中エージェントに渡します。

## 使用するもの

- [クライアント側エグレス](/ja/topics/egress) — デプロイスペックの `egress:` ブロック (モードと PAC 形式ルーティングスクリプト) で `api.anthropic.com` だけを呼び出し元ネットワーク経由にします。
- [資格情報ブローキング](/ja/topics/credentials) — デプロイスペックの `credentials:` block で、イメージに入れず API キーをエージェントへ配送します。
- [`cornus deploy`](/ja/cli/deploy) — 中継エグレスと資格情報取得の両方に必要な、フォアグラウンドの `--server` セッション。
- [セッション conduit](/ja/topics/remote-workflows) — エージェント自身のポートに到達する方法を選ぶ `--conduit`。
- [デプロイスペック](/ja/reference/deploy-spec) — `EgressSpec` と `CredentialSpec` のフィールドリファレンス。

## 手順

素朴な方法が失敗する理由は明確です。キーをイメージに焼き込むと、イメージをプルできる人やビルドログを読める人に漏れます。プレーン Pod-spec env var として渡すとクラスター control plane に書かれます。キーがあっても、許可された経路が corporate プロキシの背後にしかない場合、クラスター上の Pod は API へソケットを開けずタイムアウトします。

**1. キーを自分のマシンで利用可能にします (クラスターには置かない)。** Cornus は取得時に呼び出し元の環境から資格情報を発行します。ここでは `env` バックエンドが呼び出し元側変数から読み取ります。

```sh
export ANTHROPIC_API_KEY=sk-ant-...      # 自分のマシンにだけ残る
```

**2. デプロイスペックを書きます。** `egress:` ブロックは PAC スクリプトで `api.anthropic.com` だけをクライアント経由にします (それ以外は `DIRECT`、つまり Pod 自身のネットワークのままです)。`credentials:` ブロックはエージェントがすでに期待する `ANTHROPIC_API_KEY` 環境変数としてキーを仲介します。

```yaml
name: agent
image: localhost:5000/coding-agent@sha256:1c2d...   # ダイジェスト固定。シークレットは含まれない

env:
  AGENT_TASK: "triage the backlog"

egress:
  mode: proxy                 # caretaker の転送プロキシをサーバー経由で中継する
  default: cluster            # 一致しない宛先は Pod 自身のネットワークからエグレスする
  script: |
    function FindProxyForURL(url, host) {
      if (dnsDomainIs(host, "api.anthropic.com")) return "PROXY client";
      return "DIRECT";
    }

credentials:
  sources:
    - name: anthropic
      backend: env                       # 発行 from a caller-side env var
      config: { var: ANTHROPIC_API_KEY } # non-secret: only the var name travels
      deliver:
        - kind: env
          envVar: ANTHROPIC_API_KEY      # inject into the agent container
```

**3. フォアグラウンドセッションでデプロイします。** 中継エグレス (`proxy`/`transparent`) と資格情報ブローキングは稼働中 deploy-attach 接続で応答するため、これは Kubernetes バックエンドに対するフォアグラウンド `--server` デプロイです。`--detach` ではありません。

```sh
cornus deploy -f agent.yaml --server https://cornus.example.com
```

**4. 任意で conduit を選びます。** エージェント自身のポート (health エンドポイント、UI) に到達する方法を選びます。ポートごとの転送が既定で、サービス名に到達する単一 SOCKS5 プロキシが opt-in の代替です。

```sh
cornus deploy -f agent.yaml --server https://cornus.example.com --conduit socks5
```

エージェントの実行中はセッションが維持されます。`Ctrl-C` はワークロードを削除し、エグレス中継と資格情報取得への応答も停止します。

## 仕組み

一つの仕様で二つの独立した機構を組み合わせます。**エグレス** は宛先ごとのルーティング decision です。PAC script は caretaker で評価され、サーバーとクライアントでも再検査されます。そのため三者の判断は一致し、侵害された Pod が自身のルーティングを昇格させることはできません。`api.anthropic.com` は `PROXY client`、すなわち `client` 経路に対応します。caretaker の転送プロキシは接続を cornus サーバー経由で自分の machine にトンネルし、machine が corporate/VPN/SASE パスで API に接続します。その他の宛先は `DIRECT` を返し、`cluster` に対応します。Pod からローカルに接続され、中継されません。`default` が `cluster` なのでエグレスを有効にしてもクラスター内トラフィックが暗黙に迂回されません。API 宛先だけをクライアントへ*出す*ことを opt-in します。全経路 table (`client`、`gateway`、`cluster`、`deny`) と PAC return mapping は[クライアント側エグレス](/ja/topics/egress)と[`EgressSpec` リファレンス](/ja/reference/deploy-spec)にあります。

**資格情報ブローキング**は直交します。バックエンド名と非シークレット `config` (`{ var: ANTHROPIC_API_KEY }`) だけがサーバーへ渡ります。キー自体は取得時に自分のマシンで生成され、Pod ごとの caretaker サイドカーが配送します。エージェントは通常どおりキーを使って `api.anthropic.com` を呼び出し、その外向き要求がエグレスポリシーによりネットワーク経由になります。したがって二つの機能はエージェント自身の HTTPS 要求で交わります。資格情報がキーをエージェントに渡し、エグレスがパケットを自分のネットワークへ運びます。

信頼モデルは次のとおりです。キーはイメージ (ダイジェスト固定でシークレットなし) にも、デプロイスペックにも、通信制御フレームにもありません。稼働中セッション上で取得ごとに応答され、ワークロードが取得できるのは自身のデプロイセッションが宣言した資格情報名だけです。サーバー中継と caretaker の両方で検査される推測不能なセッション機能によりキー付けされます。API へのトラフィックはポリシーで単一宛先に制限されます。

## バリエーション

**キーをコンテナにまったく入れない。** 上の `env` 種別の配送はキーを一度 Kubernetes シークレットに取得します (静的で etcd に残ります)。最も強いセキュリティ要件には、代わりに `anthropic-proxy` エンドポイントプロバイダーを使います。caretaker が API へのループバックリバースプロキシを実行し、自身で認証ヘッダーを注入するため、エージェントはキーを**持ちません**。ローカル Claude Code / Codex ログインを利用することもできます。

```yaml
credentials:
  sources:
    - name: anthropic
      backend: claude-code                 # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional: an Anthropic-compatible gateway
```

**PAC ではなく規則で経路する。** PAC ファイルがなければ `script:` を順序付き `rules:` list (または `--egress-route` CLI フラグ) に置き換えます。最初の match が採用されます。

```yaml
egress:
  mode: proxy
  default: cluster
  rules:
    - { pattern: "api.anthropic.com:443", route: client }
```

**プロキシ env var を無視する app を対象にする。** `mode: proxy` を `mode: transparent` に切り替えます。すべての app TCP は nftables redirect で capture・中継されるため、エージェントにプロキシ awareness は必要ありません。

**関連項目:** [クライアント側エグレス](/ja/topics/egress) · [資格情報ブローキング](/ja/topics/credentials) · [デプロイスペック](/ja/reference/deploy-spec) · [`cornus deploy`](/ja/cli/deploy) · [エグレスレシピ](/ja/guides/egress) · [資格情報レシピ](/ja/guides/credentials) · [リモートワークフロー](/ja/topics/remote-workflows)
