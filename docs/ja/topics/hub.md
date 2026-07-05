# ワークロード間 hub

Cornus サーバーは、到達可能なネットワークを共有しないワークロード、たとえばノード間、クラスター間、NAT 内のノート PC を接続する**スター型ハブ**としても機能します。これは、クライアントのエクスポートを一つの Pod へ中継する接続仲介の仕組みを、登録済みワークロード間の任意の TCP/UDP 通信を中継する仕組みへ一般化したものです。各参加者を**スポーク**と呼びます。スポークは自分が提供するサービスを登録し、ほかのスポークが提供するサービスへ名前で到達します。パブリックインターネット向けの対となる機能 — ワークロードの 1 ポートをほかのワークロードにではなく外部に公開する機能 — については、[パブリックトンネル](/ja/topics/tunnels)を参照してください。

## CLI から参加する

`cornus hub` はどこからでもオーバーレイに参加します。たとえば NAT 内の laptop からローカルサービスをオーバーレイに提供したり、オーバーレイサービスに名前で到達したりできます。`--server` は任意で、選択中の接続プロファイルにフォールバックします (明示的な `--server` が優先)。

```sh
cornus hub --server ws://cornus.example:5000 \
  --register api=localhost:8080 --reach db=127.0.0.1:5432
```

`--register name=addr` はローカルサービスを名前付きで提供します。`--reach name=local-addr` は、その名前のオーバーレイサービスへ転送するローカルアドレスをバインドします。[`cornus hub`](/ja/cli/hub) を参照してください。(カスタム CA、mTLS cert、insecure-skip-verify などの client-TLS material を持つプロファイルは、今のところ `hub` では拒否されます。system trust ストアが受け付けるサーバー証明書を使ってください。)

## 中継 model

spoke は各サービスを 2 つのモードのどちらかで登録します。

- **dial-direct** - hub が到達できるアドレスとともに登録され、hub が自分で接続します。
- **配送** - アドレスなしで登録されます。hub は hosting spoke へ*戻る* イングレスストリームを開いてサービスに到達し、hosting spoke が自分のローカル対象に接続して splice します。これにより NAT 内や cross-cluster の対象に到達できます。hub がそれらへの経路を持つ必要はありません。

peer に到達するには、ソース spoke がサービス名を指定した data ストリームを開きます。hub はそれを lookup し、直接なら接続、配送なら owning spoke 経由で deliver し、byte を splice します。**TCP と UDP** の両方が動きます。中継は単に byte をコピーするだけなので、UDP は 2 つの変換点で framing するだけで済みます。これは `/udp` ポート接尾辞で選択します。トラフィックは `app -> caretaker -> hub -> {dial | ingress to spoke}` と流れます。

## デプロイスペックでの discovery

Kubernetes にデプロイされたワークロードでは、hub membership は CLI ではなくデプロイスペックの `hub:` block で宣言します。`export` はワークロードがホストするサービスを列挙し、`import` は到達するサービスを列挙します。バックエンドはインポートごとに synthetic loopback IP を割り当て、DNS record と caretaker リスナーをそこへ配線します。そのため app 内の普通の `dial(peer)` は synthetic IP に解決され、application が意識しなくても hub に流れ込みます。

```yaml
name: api
image: localhost:5000/api:v1
hub:
  identity: api                 # policy identity (defaults to the deployment name)
  export:
    - { name: api, port: 8080 }
    - { name: udpecho, port: 9000, protocol: udp, deliver: true }
  import:
    - { name: db, ports: [5432] }
```

サービスが hub から到達可能でない場合は、エクスポートに `deliver: true` を設定します (hub は pod へ中継し、pod が localhost のポートに接続します)。`importDynamic` はワークロードを動的 discovery に opt in します。静的な `import` list の代わりに、caretaker が hub catalog プッシュを subscribe し、サービスの出現/消滅に合わせて、catalog に載るすべてのサービスの deterministic synthetic IP にリスナーをバインドします。完全なフィールドリファレンスは [デプロイスペック](/ja/reference/deploy-spec) にあります。

## ポリシー

access は任意設定の 2 つの matrix によって管理され、それぞれ設定された場合にのみ強制されます。1 つは **reach** matrix (呼び出し元 ID から許可された callee サービスへ、`CORNUS_HUB_POLICY`) で、もう 1 つは **登録** matrix (ID からホスト可能なサービス name へ、`CORNUS_HUB_REGISTER_POLICY`) です。spoke は control ストリーム上で ID を宣言しますが、mTLS では verified クライアント証明書の CommonName が authoritative な ID として使われます。そのためポリシーは spoke が偽造できない資格情報に基づきます。ID の確立方法は [auth and TLS](/ja/topics/auth-and-tls) を参照してください。

## 複数レプリカの実行

ハブは単一レプリカ (既定のメモリ内レジストリ。多くのデプロイメントでは十分) または高可用性と接続数の拡張を目的とする複数レプリカで実行できます。各レプリカは自分に接続しているスポークについて唯一の管理者となるため、レプリカは互いに重ならないレジストリ区画を所有し、その集合が分散型レジストリになります。書き込み競合も CRDT の統合もありません。停止したレプリカの区画全体は消え、スポークはロードバランサー経由で新しい管理者の下へ再接続します。ストアは `CORNUS_HUB_REDIS` (一つの Redis を共有する二つのレプリカが一つのハブになる)、次に `CORNUS_HUB_STORE=kube` (Kubernetes API サーバーを Lease 対応レジストリとして使うため外部基盤は不要)、それ以外ではメモリ内の単一レプリカレジストリです。配送の検索が管理者ではないレプリカに着地した場合、認証済み内部エンドポイントを介して管理者へ転送するため、二段階の配送経路になります。
