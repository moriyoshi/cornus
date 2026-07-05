# ネットワーク: ポート転送、トンネル、イングレス、hub

ワークロードへの通信とワークロード間の通信を運ぶ仕組みは 4 つあり、それぞれ異なる方向を対象にします。**ポート転送** はワークロードのポートを呼び出し元のマシンへ持ってきます。**パブリックトンネル** はそれをインターネットに公開します。**イングレス** はクラスターにネイティブな入口です。そして **hub** はネットワークをまたいでワークロード同士を接続します。これらは下層の転送経路を共有します。イングレス以外はすべて、同じサーバー中継のバイトトンネルに乗ります。

## ポート転送

`cornus port-forward --server <url> <name> [LOCAL:]REMOTE ...` はローカル TCP ポートをデプロイメントの最初のインスタンスにあるコンテナポートへ転送します。CLI は対応付けごとにローカルリスナーを 1 つバインドし、受け付けた接続ごとにサーバーへの独自の WebSocket トンネルを開きます。サーバーはそれをバックエンドへ中継します。そのため、この機能はバックエンドに依存せず、ワークロードが公開していないポートにも到達できます。

- **dockerhost** はコンテナを確認して IP を取得し、直接接続します。これはサーバーが Docker bridge へ経路できることを仮定します。サーバーが Docker ホスト上、または Docker ホストと一緒に動く通常のデプロイメントでは成立します。
- **kubernetes** は API サーバー経由で `pods/portforward` SPDY subresource に乗ります。そのため out-of-cluster kubeconfig からでも、sidecar なし、pod ネットワークへの経路なしで動きます。Kubernetes ポート転送は TCP-only です。
- **containerd** はインスタンスに記録された CNI IP へ直接接続します。

クラスター接続プロファイルでは、クライアントは既定で Kubernetes の転送をサーバー経由にしません。サーバー自身の ServiceAccount には通常 `pods/portforward` RBAC がないため、サーバー中継の転送は黙って失敗しがちです。代わりにクライアントは開発者自身の kubeconfig でワークロード Pod へ直接接続し、サーバー中継の経路はトラフィック開始前のフォールバックとしてだけ保持します。`via-server` プロファイルの切り替えはサーバー経由の経路を強制します。同じ直接接続優先・プロキシフォールバック規則はワークロードログにも適用されます。

**UDP 対応付け** (`5353:53/udp`) は dockerhost と containerd バックエンドで動作します。トンネルは長さでフレーミングしたデータグラムを運び、クライアントの送信元アドレスごとに 1 トンネルを設け、アイドルタイムアウトを適用します。サーバーは最初のフレームの前に UDP トンネルを確認応答するため、対応していないバックエンドや古いサーバーは接続を適切に拒否できます。

**公開済みポートは自動で転送されます。** すべてのクライアントセッション対象範囲、つまり `cornus deploy --server`、`cornus compose up` (フォアグラウンドまたは `-d`)、docker frontend は、同じエンジンを通して `DeploySpec.Ports` を publish します。そのためすべてのバックエンドで、`host:` ポートは「クライアントの `127.0.0.1:<host>` で到達可能」という意味になります。各対象範囲は `--no-forward-ports` を取ります。スキップ (転送不能な UDP mapping、すでにバインド済みのローカルポート) は警告して継続し、セッションを失敗させません。ワークフローは [networking ガイド](/ja/guides/networking) を参照してください。

## パブリックトンネル

ポート転送が呼び出し元にローカルリスナーを渡すのに対し、`cornus tunnel <name> <port>` は **パブリック URL** を返します。サーバーはトンネルをプロセス内でホストし、各 inbound 接続を、ポート転送が使うものと*同じ* byte-bridge 経由でワークロードへ bridge します。そのため任意のバックエンドで unpublished ポートに到達でき、トンネルはその bridge の前に置かれた hosted 中継にすぎません。

クライアントはすでに認証済みの要求にトンネル資格情報を注入します。サーバーは事前に資格情報を知りません (運用者がサーバー側既定を設定することはできます)。provider seam には 2 つの形があります。1 つはバックエンドがサーバーが accept できる本物のリスナーを返す **リスナー model** (ngrok) です。もう 1 つはバックエンドがローカル URL へ転送することしかできない **upstream model** (cloudflared、tailscale) で、この場合サーバーは loopback shim を立て、そのアドレスをバックエンドに渡します。4 つのバックエンドが同梱されています。ngrok (プロセス内、既定)、ssh (fail-closed host-key verification 付き remote-forward。sish、serveo、またはプレーン sshd で動作)、cloudflare、tailscale です。各バックエンドの必要条件は [バックエンド表](/ja/guides/tunnels#バックエンド) を、段階的な設定手順は [トンネルガイド](/ja/guides/tunnels) を参照してください。

## 自動イングレス (Kubernetes のみ)

トンネルが invocation ごとにサーバーが実行する hosted 中継であるのに対し、**イングレス** は cluster-native な front door です。`DeploySpec.Ingress` が opt in すると、kubernetes バックエンドは ClusterIP サービスと並べて標準イングレスを作成し、デプロイメントへの owner 参照を付けるため delete 時に reap されます。他のバックエンドは警告をログしてフィールドを無視します。そのため Compose ファイルは portable のままです。

特徴的な性質は、一時的 preview 環境を狙った **自動ホスト derivation** です。サーバーに base ドメイン (`CORNUS_INGRESS_DOMAIN`、加えて `CORNUS_INGRESS_CLASS` と `CORNUS_INGRESS_TLS_ISSUER`) が設定されている場合、イングレスを単に*有効化*したデプロイはパブリック URL を無料で得ます。compose translator は `<service>.<project>` を導出し、バックエンドはそれをドメインの前に置きます。つまり `web.pr-123.<domain>` です。これにより多数のプロジェクトが 1 つの wildcard ドメイン上で共存できます。すべての既定は仕様内でクライアントが上書きできます。また multi-tenant サーバーでは `CORNUS_INGRESS_ENFORCE_DOMAIN` でドメインを固定でき、resolved ホストがその外に出る場合は拒否されます。クライアントが任意の hostname を奪うことはできません。完全なフィールドリファレンスは [イングレス](/ja/guides/ingress) を参照してください。

トンネルとの trade-off は次の通りです。イングレスは cluster-native で detached デプロイ後も生き続けますが、イングレス controller と wildcard DNS (HTTPS には cert-issuer) が必要です。`cornus tunnel` はそれらを必要とせず任意のバックエンドで動きますが、コマンドが生きている間だけ存在します。

## セッション conduit: ポート転送または SOCKS5

自動転送は、クライアントセッションがワークロードを呼び出し元へ公開する既定の **conduit モード** です。opt-in の代替は、ポートごとのローカルリスナーを単一のクライアント側 **SOCKS5 スプリットトンネルプロキシ** に置き換えます。CONNECT 対象は resolution 規則と照合され、match すれば `service:port` に書き換えられて同じ転送経路で内向きにトンネルされます。match しなければ呼び出し元のホストから直接接続されます。クラスター name は内へ入り、それ以外は通常どおりエグレスします。

日常的な既定規則は service-host 接尾辞を削ります。`web.cornus.internal:8080` はサービス `web` に到達します。そのため 1 つのプロキシが、ポートを事前宣言せずにすべてのサービスへ名前で到達できます。セッション alias table はさらに、短い compose サービス name を project-prefix 付きデプロイメントに map します。そのため素の `web:8080` も、稼働中サービスを曖昧さなく指す場合は内向きに経路されます。モードはセッションごとに解決されます (`--conduit` フラグ、`CORNUS_CONDUIT`、または接続プロファイル)。共有プロキシは同じ接続上の compose サービスと docker コンテナにまたがります。`cornus socks5` は同じプロキシを単独で実行します。SOCKS5 CONNECT は TCP-only です。設定と precedence は [ネットワークと conduit](/ja/guides/networking) を参照してください。

## ワークロード間 hub

サーバーは、routable ネットワークを共有しないワークロード、つまり cross-node、cross-cluster、または NAT 内の laptop を接続する **star hub** としても機能します。各 participant は **spoke** です (pod の caretaker、または CLI からの `cornus hub`)。spoke は自分がホストするサービスを登録し、他の spoke のサービスに名前で到達します。

### 中継 model

spoke は各サービスを 2 つのモードのどちらかで登録します。

- **dial-direct** - hub が到達できるアドレスとともに登録され、hub が自分で接続します。
- **配送** - アドレスなしで登録されます。hub は hosting spoke へ*戻る* イングレスストリームを開いてサービスに到達し、hosting spoke が自分のローカル対象に接続して splice します。これにより NAT 内や cross-cluster の対象に到達できます。hub がそれらへの経路を持つ必要はありません。

spoke ごとに 1 本の接続が control、エグレスストリーム、イングレスストリームを運びます。トラフィックは `app -> caretaker -> hub -> {dial | ingress to spoke}` と流れます。中継は byte-agnostic なので、**TCP と UDP** の両方が動きます。datagram はストリーム上で length-framed され、両端で元に戻されます。

### discovery とポリシー

インポートされた peer は、決定的に **synthetic `127.0.0.0/8` IP** へ map されます。Kubernetes では caretaker の DNS 役割が各 peer の name を、その loopback リスナーがバインドするのと同じ synthetic IP で提供します。そのため app の通常の `dial(peer)` は、application が意識しなくても hub へ流れ込みます。インポートは **動的** にできます。`watch` capability を持つ spoke は、control ストリーム上でプッシュされる catalog update を受け取り、検出されたサービスごとにリスナーをバインドします。

ポリシーは任意設定の 2 つの matrix であり、設定された場合にのみ強制されます。1 つは **reach** matrix (呼び出し元 ID から許可された callee サービスへ、`CORNUS_HUB_POLICY`) で、もう 1 つは **登録** matrix (ID からホスト可能な name へ、`CORNUS_HUB_REGISTER_POLICY`) です。mTLS では ID は verified クライアント証明書から取得されるため、ポリシーは spoke が偽造できない資格情報に基づきます。

### 複数レプリカの実行

配送対象は spoke への稼働中セッションを保持します。そして spoke 接続を保持しているレプリカだけが、その spoke へのイングレスストリームを開けます。負荷を支える単純化は次の通りです。各レプリカは自分に接続している spoke についての*唯一の authority* なので、レプリカは **互いに素なレジストリ partition** を所有します。distributed レジストリはその union にすぎず、write conflict も統合 logic もありません。dead レプリカの partition は消えます。その spoke は load balancer 経由で reconnect し、新しい owner の下で re-register します。

共有ストアは 2 つ同梱されています。**Redis** (`CORNUS_HUB_REDIS`; liveness は TTL 付き heartbeat キー) と **Kubernetes-native ストア** (`CORNUS_HUB_STORE=kube`; provider はカスタム resource、liveness は Lease、GC は owner 参照に乗るため外部 infrastructure 不要) です。lookup が配送 owner ではないレプリカに着地した場合、そのレプリカは owner へ転送して splice するため、two-hop パスになります。率直に言えば、hub は control-plane 中継であり、ほとんどのデプロイメントには単一レプリカで十分です。multi-replica は、その extra hop と引き換えにオーバーレイの HA と connection-count スケールを得るものです。

### マウントと資格情報の転送

マウント中継と資格情報中継も、この同じ転送パスに乗ります。そして同じルーティングレコードにも乗ります。クライアント側アタッチメントを持つ deploy-attach セッションは、予約された `cornus.mount/<digest>` という name の下にレコードを 1 つだけ公開します (生の id は capability なので、載るのはセッション id のダイジェストだけです)。誤ったレプリカに着地した caretaker ストリームは、`GET /.cornus/v1/mount/forward` または `GET /.cornus/v1/cred/forward` を通じて owner へ splice されます。資格情報専用の別レコードは意図的に設けていません。1 つの deploy-attach セッションが pod のマウントと資格情報の両方を所有するため、資格情報のルーティングは、同期を保つべき第 2 のライフサイクルを重複して抱えるかわりに、マウントレコードの TTL / Lease による liveness とクラッシュ時 GC をそのまま継承します。

この 2 つの hop の認可は、**owner** レプリカで強制されます。owner は自分のローカルレジストリでセッションを解決し直し、要求されたマウント名または資格情報名を、その稼働中セッションの許可リストと照合し直します。転送側のレプリカはセッション状態を一切持たず、問い合わせられることもなく、name を保証することもできないため、偽造された転送でこのチェックを迂回することはできません。これは hub の配送転送とは逆向きです。配送転送では reach ポリシーが転送側で強制され、owner はそれを信頼します。reach ポリシーは呼び出し元 ID に基づいており、それを認証したのは転送側だけであるのに対し、資格情報の権限は owner だけが保持するセッション単位の capability だからです。どちらのハンドラーも再転送を行わないため、stale なレコードがループを形成することはありません。

## 関連ページ

- [ネットワークと conduit](/ja/guides/networking) - conduit モードに加え、転送とワークロードへの到達方法のレシピ。
- [トンネル](/ja/guides/tunnels) - トンネルバックエンドと段階的な設定手順。
- [ワークロード間 hub](/ja/guides/hub) - オーバーレイの中継 model と使い方。
- [イングレス](/ja/guides/ingress) - イングレスフィールドリファレンス。
- [リモートクラスターで作業する](/ja/guides/remote-clusters) - 接続プロファイル。
- [cornus port-forward](/ja/cli/port-forward) · [cornus tunnel](/ja/cli/tunnel) · [cornus socks5](/ja/cli/socks5) · [cornus hub](/ja/cli/hub)
