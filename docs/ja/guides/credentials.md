# 資格情報

リモートワークロードに、呼び出し元が発行したシークレット (クラウド資格情報、LLM API キーなど) を渡すためのタスク指向レシピです。シークレットをイメージ、仕様、Pod 仕様へ焼き込む必要はありません。仕組みについては、[資格情報ブローキング](/ja/topics/credentials)と[デプロイスペック](/ja/reference/deploy-spec)を参照してください。ワークロードの外向き通信を呼び出し元経由にする場合は、[エグレス](/ja/guides/egress)ガイドを参照してください。

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

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)、[資格情報ブローキング](/ja/topics/credentials)

## LLM API をプロキシする、または API キーをワークロードへ注入する

caretaker のリバースプロキシを通じてワークロードから LLM を呼び、プロキシが認証ヘッダーを注入するようにします。キーはコンテナに入りません。

```yaml
credentials:
  sources:
    - name: claude
      backend: claude-code                  # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional Anthropic-/OpenAI-compatible gateway
```

- `anthropic-proxy` / `openai-proxy` プロバイダーは `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` を設定し、クライアントが送った認証情報を除去して、要求ごとに本物の資格情報を追加します。
- 単純な環境変数を注入するには、`backend: env` を `config.var` とともに使い、`env` 種別で配送します (静的な資格情報は Kubernetes シークレットに保存されます。短期間有効なシークレットには `endpoint` / `file` を推奨します)。

**関連項目:** [資格情報ブローキング](/ja/topics/credentials)、[デプロイスペック](/ja/reference/deploy-spec)

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
- `provider: aws-imds` は未変更の AWS SDK が取得できる ECS / IMDSv2 形式で資格情報を提供します。loopback (既定) では `AWS_CONTAINER_CREDENTIALS_FULL_URI` (ECS 資格情報エンドポイント) を注入します。`wellKnown: true` は代わりに Pod 内で `169.254.169.254` (IMDSv2) をバインドし、環境変数を使いません (`NET_ADMIN` が必要です)。

**関連項目:** [資格情報ブローキング](/ja/topics/credentials)、[デプロイスペック](/ja/reference/deploy-spec)
