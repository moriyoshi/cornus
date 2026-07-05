# サーバー、レジストリ、コンテンツストア

1 つの HTTP プロセスがすべてを提供します。単一のマルチプレクサーがレジストリを `/v2/*` の下に、ビルド API とデプロイ API を `/.cornus/v1/*` の下に、稼働・準備確認を `/healthz` と `/readyz` の下に経路します。ビルドエンジンとデプロイバックエンドは初回利用時に遅延構築されるため、運用者は他サブシステムの前提条件が存在しない環境でも、レジストリ専用またはデプロイ専用のサーバーを実行できます。

## 運用上の保護策

運用上の保護策はサーバーにあります。

- **準備確認は実際の状態を示します。** `/readyz` はアトミックに稼働状態へ切り替わり、シャットダウン時には 503 へ戻ります。`/healthz` は純粋に稼働確認だけを行います。
- **並行実行と直列化。** ビルドは `CORNUS_BUILD_CONCURRENCY` セマフォ (既定は CPU 数) の下で実行されます。同じデプロイメント名に対する適用と削除は名前ごとのミューテックスで直列化されるため、2 つの呼び出し元が同じワークロードで競合することはありません。
- **要求サイズの上限。** ビルドコンテキストの tar は 2 GiB (`CORNUS_MAX_BUILD_CONTEXT_BYTES`) に、ブロブの PUT は 10 GiB に制限されます。上限超過時には 413 を返し、アップロードセッションを中止します。
- **ストリーム内のビルド失敗。** ビルドは HTTP 200 が送られた後に出力をストリームするため、ストリームの途中で起きた失敗は本文内の `BUILD FAILED:` トレーラーとして届きます。クライアントはストリームを走査しなければなりません。状態コードだけが正本ではありません。
- **Deploy-side ストリームエラーは隠されず対象範囲されます。** ログ、stats、archive download はバックエンドの最初の出力 byte が来るまで 200 の書き込みを遅らせます。そのため出力前の failure はエラー body 付きの本物の 4xx/5xx になります。出力開始後のエラーは `X-Cornus-Stream-Error` HTTP trailer に付与され、Cornus クライアントは partial byte を届けつつ、EOF 後にそれを検査します。
- **Fail-closed 設定。** malformed ポリシー環境 (`CORNUS_API_POLICY`、`CORNUS_HUB_POLICY`、`CORNUS_HUB_REGISTER_POLICY`) は hard startup エラーであり、fail-open にはなりません。

shutdown は lazily-built エンジンとデプロイバックエンドを close し、ビルドエンジンの data-dir lock を解放します。

## レジストリ

レジストリは、永続的なコンテンツアドレス指定ストアに対して直接書かれた、独自実装の OCI Distribution v1.1 ハンドラー群です。マニフェストとタグは再起動後も残ります。これが、一般的なメモリ内レジストリライブラリを使えない理由です。対応する機能は仕様の実用的な一部です。ping、ブロブ HEAD/GET (`Range` 対応)、monolithic/chunked/cross-repo-mount ブロブ upload、ブロブとマニフェストの削除 (マニフェストは digest 指定のみ)、マニフェスト PUT/GET、ページ分割されたタグと `_catalog` の一覧、Referrers API を提供します。

内部の分割は意図的です。コンテンツストアは sha256 addressing、digest verification、upload staging、manifest/tag/repo indexing を所有します。レジストリレイヤーはその上に薄く置かれた OCI-protocol HTTP handler set です。

## 差し替え可能な永続化

永続化は差し替え可能な拡張点であり、意図的に小さい抽象化を採用しています。バックエンドは最小限の `ObjectStore` (`Get` / `Put` / `Stat` / `Delete` / `List`) だけを実装し、レジストリの意味論は*すべて*そのインターフェースの上に 1 回だけ存在します。コンテンツアドレス指定キーのレイアウト、つまりディレクトリやバケットに実際に置かれるものは次の通りです。

```
blobs/sha256/<aa>/<hex>          blob content
repos/<repo>/manifests/<hex>     value = media type
repos/<repo>/tags/<tag>          value = digest
```

2 つのバックエンドが同梱されています。**ファイルシステム** バックエンドはネイティブで依存関係のない既定です。**bucket** バックエンドは gocloud bucket をラップし、`mem://`、`s3://`、そして `-tags cloudblob` ビルドでは (それらの driver が Google/Azure SDK を引き込むため) `gs://` と `azblob://` を提供します。MinIO などの S3-compatible サーバーには、カスタムエンドポイントと path-style addressing を持つ明示的なクライアントが用意されます。バックエンドは `cornus serve --storage <ref>` / `CORNUS_STORAGE` で選択します。空のは on-disk data-dir layout に既定します。設定は [ストレージ backends](/ja/reference/storage-backends) を参照してください。

**Resumable upload は capability-based です。** ネイティブ uploading を実装するバックエンドは OCI PATCH/PUT upload flow を自分で処理します。それ以外はローカル staging にフォールバックします。ファイルシステムバックエンドはセッションファイルに append し、ブロブパスへの rename で commit します。興味深いのは S3 バックエンドです。各 OCI PATCH は別々の HTTP 要求なので、すべての upload 状態はサーバー側に存在する必要があります。part は S3 multipart upload へストリームされ、小さな JSON sidecar object が part ETag、pending tail、running sha256 状態を保持します。これによりブロブ size に関係なくローカル staging は 5 MiB 未満に保たれます。

## ミス時のフォールバック: プルスルーミラーとローカルストアの再エクスポート

`/v2/*` のマニフェストまたはブロブのミスは、404 を返す代わりに読み取り専用のソースへ
フォールバックできます。ハンドラーはまずローカルストアを調べ、その後で設定済みの
ソースを参照します。ソースは相互排他です。

- **プルスルーミラー** (`CORNUS_REGISTRY_MIRROR=<host>`) 。ミスはアップストリーム OCI
  レジストリから取得して提供します。`CORNUS_REGISTRY_MIRROR_CACHE` (既定でオン) を
  指定すると、取得したものをストアにも永続化するので、以降のプルはローカルで
  解決します。
- **ローカルストアの再エクスポート** (`CORNUS_REGISTRY_SOURCE=host-native`。ホスト
  バックエンドでは **既定**) 。ローカルの Docker または containerd ホストに対して
  開発するとき、イメージはたいてい既にローカルにあるため、別個の cornus レジストリに
  コピーを持つのは冗長です。これは `/v2/*` をそのローカルストアの*ビュー*にします
  (バックエンドごと) 。**`containerd`** では `/v2/*` をホスト containerd のネイティブな
  コンテンツストアで **読み書き可能** に支えます。push はストアへ直接インポートし
  (digest 単位のブロブ + イメージレコード) 、プルはそれを読み戻すため、`/v2/*` へ push する
  `cornus build` は即座にデプロイ可能です — ビルドワーカーの設定は不要です。
  **`dockerhost`** ではローカル Docker デーモンの **読み取り専用** ビューです
  (ミスは `docker save` 経由で提供) 。同一ホストのデプロイはデーモンが既に持つイメージの
  レジストリプルをスキップし、従来の Docker には書き込み可能なコンテンツストアがないため
  `/v2/*` への push は `405` で拒否されます — `cornus build` はサーバー経由でルートされ、
  サーバーが結果を `docker load` でデーモンへ取り込みます。

`--storage` を指定しない場合、host-native は **独立した CAS を保持しません**。
`_catalog` / タグ一覧はローカルストアのみを反映し、ライフサイクルはランタイムの仕事です。
docker デーモンビューの `docker save` はダイジェストを再計算します (タグでプル) 。containerd
ビューはそれを保持します。従来の push 可能なレジストリを維持するには、
`CORNUS_REGISTRY_SOURCE=off` を設定するか、明示的な `--storage` を渡します (CAS + ソースの
ユニオンビュー) 。設定済みのミラー、または非ホストバックエンドもこれを維持します。これらの
モードはローカル開発向けであり、共有の高ファンアウトレジストリ向けではありません。
[ローカルイメージストアの
再利用](/ja/reference/server-env-vars#reusing-a-local-image-store) を参照。

## ガベージコレクションとクラッシュ時の安全性

ストレージ GC は on-demand の **mark-and-sweep** です。root は各 repo のタグとマニフェスト marker です。mark phase はマニフェストと index (設定、レイヤー、nested `manifests[]`、`subject`) を解析し、sweep は unreachable ブロブを削除します。`POST /.cornus/v1/gc` がこれを trigger し (`gc` ポリシー操作で gate)、ビルドエンジンのローカルキャッシュも 7-day TTL で prune します。

`CORNUS_GC_INTERVAL` (Go duration) を設定すると、同じ GC がバックグラウンドで定期実行されます。unset なら完全に無効です。一方、malformed または non-positive 値は hard startup エラーです。schedule の typo が reclamation を黙って無効化してはいけません。レプリカが複数ある場合、`CORNUS_GC_LEASE` は各 tick を Kubernetes `coordination.k8s.io` Lease への compare-and-swap の背後で gate します。そのためレプリカが同時に sweep することはありません。acquire が拒否された場合は tick をスキップするだけです (sweep の missed は concurrent sweep よりましです)。stale upload staging は startup 時に 24-hour TTL で sweep されます。GC の運用は [レジストリガイド](/ja/guides/registry) を参照してください。

crash 後の repair pass は不要です。マニフェスト write は依存関係 order、つまり **ブロブ、次にマニフェスト marker、最後にタグ** の順に行われます。そのため crash が残すのは最悪でも GC で reclaim 可能な orphan であり、missing data を指すタグではありません。

## 関連ページ

- [レジストリ & ストレージガイド](/ja/guides/registry) - serving、advertising、GC の実践。
- [ストレージ backends](/ja/reference/storage-backends) - 各バックエンドの設定。
- [サーバー env vars](/ja/reference/server-env-vars) - 環境対象範囲全体。
- [cornus serve](/ja/cli/serve) - 提供コマンド。
