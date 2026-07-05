# セキュリティモデル

セキュリティは層状で、**明示的に有効化する方式**です。何も設定しない Cornus は、ローカル開発向けのパススルーとして動作します。以下の各レイヤーは設定で有効になり、**有効化後はフェイルクローズ**します。不正なポリシーは起動時の重大なエラーとなり、ID が必要な場所では空の ID を拒否し、設定を誤った検証器は通過させず拒否します。このページではモデルを説明します。設定と強化のレシピは[セキュリティと認証](/ja/guides/security)ガイドを参照してください。

## 認証

認証は HTTP の対象範囲全体を囲むミドルウェアの接続点です。テレメトリハンドラーの内部に組み込まれるため、拒否された要求もトレースされます。検証器が未設定ならパススルーです。有効時も `/healthz` と `/readyz` は開いたままです。その他の経路はベアラー認証を必要とします。ただし `CORNUS_REGISTRY_ANONYMOUS_PULL` 設定時の `/v2/*` 下の `GET`/`HEAD` は例外です。検証器の設定は環境変数で行います。

| 変数 | 方法 |
|---|---|
| `CORNUS_AUTH_TOKEN` | constant-time 比較する opaque 全権限 bearer トークン |
| `CORNUS_JWT_HS256_SECRET` | HS256 JWT |
| `CORNUS_JWT_PUBLIC_KEY` | PEM 公開鍵による RS256/ES256 JWT |
| `CORNUS_JWT_JWKS_FILE` / `_URL` | `kid` 選択と rotation を持つ JWKS (asymmetric のみ) |
| `CORNUS_JWT_ISSUER` / `_AUDIENCE` | 任意の registered-claim 検査 |
| `CORNUS_CARETAKER_TOKEN` | caretaker attach エンドポイントでのみ受け付ける範囲限定静的トークン |

JWT 検証は各キーを許可されるアルゴリズムの集合に結び付けます。`alg: none`、アルゴリズム混同、公開鍵を HMAC 鍵として使う手法はすべて拒否されます。認可用に呼び出し元 ID を要求コンテキストに保存します。サーバーは検証のみを行います。トークン発行 (`cornus token issue`) は HTTP の発行エンドポイントではなく、運用者または CLI による操作です。Kubernetes の caretaker サイドカーには、すべての Pod に全権限トークンを入れる代わりに、Kubernetes シークレットから取得する**範囲限定**資格情報 (caretaker attach エンドポイント専用) を与えます。

レジストリはさらに `docker login` を話します。`/v2/*` だけでは、同じ資格情報を password とする HTTP Basic を受け付けます。静的トークンまたは JWT を使い、username は無視されます (`docker login -u token -p $CORNUS_TOKEN`)。同一 verifier chain に流れます。レジストリの 401 challenge は `Bearer` ではなく `Basic realm="cornus"` です。Cornus にトークンサービスはないため、Bearer challenge では docker が存在しないトークン realm に向かいます。Basic なら通常の docker/podman が保存済み login で再試行します。非レジストリ経路は引き続き `Bearer` challenge を返し、Basic 形式の caretaker-scope 資格情報もレジストリでは拒否されます。

## TLS と mTLS ID

TLS の提供機能は `cornus serve` の `--tls-cert`/`--tls-key` に組み込まれています。更新時刻が進むとファイルを再読み込みするコールバックで提供するため、外部ローテーター (cert-manager、Vault、SPIFFE) は再起動なしにマウント済みの証明書をその場で更新できます。

mTLS クライアント証明書 ID は追加の認証方法です。検証済みクライアント証明書は CommonName を呼び出し元 ID とする完全な資格情報であり、ベアラートークンより優先します。hub もこの認証済み ID を使うため、hub の到達・登録ポリシーは spoke が偽造できない資格情報をキーにします。

## 認可

ID ごとの API 認可は、認証の上にある configure-to-enforce matrix です。`CORNUS_API_POLICY` は ID を許可操作 (`build`、`deploy`、`exec`、`push`、`pull`、`gc`) に map します。未設定なら allow-all です。設定後は呼び出し元が要求操作に列挙されていなければならず、**空の ID は拒否**されます。したがって enforcement には JWT `sub` または mTLS CommonName が実質必要です。pure read (デプロイ状態、ログ、レジストリプル) は既定で開いており、ID ごとの認可ではなく認証により管理されます。二つの補足があります。

- **`exec` は独自の操作です。** ポリシーが `exec` *または* `deploy` を許可すると exec/attach が許可されます。デプロイは exec を含みます。操作の価値は、ワークロードの apply/delete はできず実行中ワークロードに shell だけ入れる exec-only ID です。
- **レジストリプル認可は opt-in です。** 規則が `pull` 操作を明示的に言及する場合 (`"*"` wildcard は数えない)、レジストリ `GET`/`HEAD` に必要になります。明示的プルポリシーは `CORNUS_REGISTRY_ANONYMOUS_PULL` より優先します。匿名呼び出し元は ID を持たず拒否されます。両方設定時にはサーバーが起動時警告を出します。

デプロイバックエンドは defense in depth として**ワークロード privilege ポリシー**も enforcement します。ホストバックエンドは `CORNUS_ALLOW_PRIVILEGED` / `CORNUS_ALLOW_BIND_SOURCES` で opt-in しない限り `Privileged` とホストバインドソースを拒否します。kubernetes バックエンドは user-requested 特権ワークロードを既定拒否し、kernel 9P マウントまたはネットワーク redirection に本当に privilege が必要な Cornus 所有の injected sidecar は許可します。

## 信頼境界

サブシステムごとに記載された境界のうち、まとめておくべきものは次のとおりです。

- **Remote-build エクスポートは読み取り専用で閉じ込められます。** リモート builder に与える 9P access はコンテキスト、dockerfile、named-context ディレクトリだけです。`..`、symlink escape、write はなく、呼び出し元から byte が離れる前に `.dockerignore` が適用されます。[ビルドエンジン](/ja/architecture/build-engine#信頼境界)を参照。
- **セッション id は capability です。** deploy-attach セッション id は推測不能で、URL ではなく認証済みストリーム内を移動します。マウント中継が公開するのは digest だけです。
- **エグレスポリシーは hop ごとに再評価されます。** caretaker、サーバー、クライアントがそれぞれルーティングポリシーを検査するため、侵害された Pod は自身のルーティングを昇格できません。セッションなしのエグレスは operator-gated gateway 経路にだけ適用されます。[クライアント側エグレス](/ja/architecture/caretaker#クライアント側-egress)を参照。
- **Hub ポリシーは検証済み ID をキーにします。** mTLS 下の spoke ID は自身の declaration ではなくクライアント証明書から得ます。[hub](/ja/architecture/networking#discovery-and-policy)を参照。
- **Pod 内 Docker エンドポイントには明示的な運用者 grant が必要です。** `docker` caretaker 役割はワークロードに deploy-engine access を与えるため、専用 client-scope トークンシークレットが設定された場合だけ有効です。[Docker エンドポイント](/ja/architecture/caretaker#docker-エンドポイント)を参照。

## 関連ページ

- [セキュリティと認証](/ja/guides/security) — すべての verifier と TLS モードの設定、および hardening レシピ。
- [cornus token](/ja/cli/token) — JWT の発行。
- [サーバー環境変数](/ja/reference/server-env-vars) — 完全なポリシー対象範囲。
