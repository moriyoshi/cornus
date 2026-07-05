# 類似ツールとの比較

開発ワークフローにおいて Cornus のカバーする範囲は他のツールと多くの部分で重なりますが、それを実現する技術の組み合わせは珍しいものです。Cornus は、レジストリ、イメージビルダー、デプロイエンジンを同時に担う**単一の自己完結型バイナリ**です。しかも、既存の `compose.yaml`、`docker` コマンド、`devcontainer.json` から三つすべてを操作できます。新しい設定 DSL は不要で、レジストリ、`buildkitd`、GitOps コントローラーをあらかじめ用意する必要もありません。この領域のツールの多くは、すでに運用しているコンポーネントを連携させます。Cornus は、それらのコンポーネント自体です。よく比較されるツールは、主に次の三つに分けられます。

## Kubernetes 向け開発ループのオーケストレーター

[Skaffold](https://skaffold.dev/)、[Tilt](https://tilt.dev/)、[DevSpace](https://www.devspace.sh/)、[Garden](https://garden.io/)、[Okteto](https://www.okteto.com/) は、クラスターに対するビルド、プッシュ、デプロイの反復を自動化します。これらは**オーケストレーター**です。利用者が用意したビルダー (`docker` / BuildKit / kaniko) を外部コマンドとして呼び出し、利用者のレジストリへプッシュし、利用者が書いたマニフェスト、Helm、kustomize を適用します。Skaffold YAML、`Tiltfile`、`devspace.yaml`、Garden のプロジェクトグラフなど、ツール固有の設定ファイルがその操作を駆動します。

Cornus は二つの点で異なります。ビルダーとレジストリを外部から呼び出すのではなく**同梱**すること、そして新しい DSL ではなく、すでに持っている Docker の成果物、つまり Compose ファイルや devcontainer を入力として使うことです。Okteto や DevSpace はソースコードをクラスター内で動く開発コンテナへ同期します。一方 Cornus はファイルを手元のマシンに置いたまま、ビルドやバインドマウントが実際に読むバイトだけを 9P でストリーミングします。

## ローカル実行とリモートクラスターをつなぐツール

[Telepresence](https://www.telepresence.io/)、[mirrord](https://mirrord.dev/)、[Gefyra](https://gefyra.dev/) は、プロセスを**ローカル**で実行しながら、あたかもクラスター**内**で動いているように扱います。実行中 Pod のトラフィック、環境、ファイル読み取りをノート PC まで取り込みます。Cornus は隣接する問題を逆方向から解きます。**ワークロードをクラスターにデプロイ**し、クラスター上の実行環境を手元から利用できるようにします。公開済みポートは `127.0.0.1` へ自動転送され、`cornus exec` と `cornus port-forward` は任意のコンテナポートへ到達できます。SOCKS5 コンジットは `*.cornus.internal` をサービス名として解決し、ワークロード間ハブは NAT やクラスター境界をまたいでサービスを接続します。

目的が「クラスターの依存先に対して自分のコードをローカルで実行する」ことであれば、mirrord や Telepresence が適しています。目的が「Compose プロジェクトをクラスターで*実行したまま*、ローカル Docker と同様の開発ループを得る」ことであれば、Cornus がそのためのツールです。

## リモートファイル同期ツール

ローカルディレクトリとリモートディレクトリの同期だけを目的とする、リモート開発ツールの大きな分類があります。その多くは、二つの同期エンジンに集約されます。[Mutagen](https://mutagen.io/) と [Syncthing](https://syncthing.net/) です。Mutagen には [mutagen-compose](https://mutagen.io/documentation/orchestration/compose/) 統合があり、2024 年に Docker に買収され、現在は Docker Desktop の同期バインドマウントの基盤になっています。いずれも、古くからある [Unison](https://github.com/bcpierce00/unison) や `rsync` (+ `lsyncd`) の系譜にあります。

Kubernetes 向け開発ツールの多くは、そのどちらかを利用しています。[ksync](https://ksync.github.io/ksync/) と [Okteto](https://www.okteto.com/) は Syncthing を使い、[Garden](https://garden.io/) のコード同期は Mutagen を使います。[DevSpace](https://www.devspace.sh/) は独自実装を同梱し、[Skaffold](https://skaffold.dev/docs/filesync/) と [Tilt](https://tilt.dev/) は変更されたファイルを実行中のコンテナへコピーします。いずれも、ツリーをリモート側へ**コピー**し、その後二つのコピーを継続的に同期するというモデルを共有しています。これによりローカル速度でのリモート読み取りとオフライン耐性を得る代わりに、二つ目の実体化されたコピー、初回の完全転送、双方向の競合解決が必要になります。

Cornus はこの分類には属しません。同期するのではなく、ファイルを**提供**します。そのため実際に近いのは、**sshfs**、**NFS**、**virtiofs** (Docker Desktop の VM バインドパス)、9P などのネットワークファイルシステムです。リモートビルドまたはクライアントローカルバインドマウントの間、呼び出し元は読み取り専用の 9P サーバーを実行し、ワークロードは呼び出し元のファイルを**その場で**読みます。正本は一つだけなので、差分や競合解決、事前コピーは発生しません。

一般的なネットワークマウントと異なるのは、転送方式と対象範囲です。9P は一つの WebSocket でトンネルされるため、両端にマウントデーモンがなくても NAT 越しに動きます。対象はコンテキスト、名前付きコンテキスト、マウントディレクトリに限定され、`.dockerignore` でフィルターされます。さらに `--lazy` を使うと必要に応じて提供されるため、ビルドやマウントが実際に触るバイトだけが転送されます。たとえば 20 MB のコンテキストでビルドが 11 バイトしか読まなければ、転送されるのも 11 バイトです。

この方式のトレードオフは同期方式と表裏一体です。未キャッシュの読み取りは手元に常駐するコピーではなく接続に依存します。そのため Cornus は、長時間のオフライン作業ではなく、開発ループでの利用を対象にしています。「ここで編集し、あちらで実行し、両側を同期し続ける」ワークフローには、Mutagen のような専用同期ツールが適しています。Cornus は同等の機能を自身の転送方式に組み込み、別の常駐プロセスを増やしません。Mutagen はネットワークポートの転送も行いますが、Cornus は接続ごとのトンネルでこれを補います。詳しくは[ネットワーク](/ja/guides/networking)を参照してください。

## Cornus が同梱するコンポーネント

| 通常は別途動かすもの | Cornus での扱い |
| --- | --- |
| [BuildKit](https://github.com/moby/buildkit) / デーモンとしての `buildkitd` | 同じ BuildKit ソルバーをプロセス内に組み込みます。完全な `buildx` 機能を備え、デーモンは不要です。 |
| [Docker レジストリ](https://github.com/distribution/distribution) (`distribution`)、[Zot](https://zotregistry.dev/)、[Harbor](https://goharbor.io/) | 差し替え可能なコンテンツストアを備える、小さな組み込み OCI Distribution v1.1 レジストリです。 |
| [Kompose](https://kompose.io/) / [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) | Compose をマニフェストへ**一度だけ**変換します。Cornus は Compose を実行中の操作面として保ちます。 |
| [nerdctl](https://github.com/containerd/nerdctl) (containerd 上の Docker CLI) | containerd デプロイバックエンドは、素の containerd ホストで Compose プロジェクトをネイティブに実行します。Docker と Kubernetes も対象にできます。 |
| ローカルデーモンに対する標準 `docker` / `docker compose` | 同じコマンドをリモート Cornus サーバーへ振り向けます (`cornus daemon docker`、`cornus compose`)。ファイルは手元のマシンからストリーミングされます。 |

最も近い単一バイナリの類例は [Werf](https://werf.io/) です。Werf も一つのバイナリから Kubernetes へのビルドとデプロイを行います。ただし Werf は Git 駆動であり、外部レジストリと Helm による適用に依存します。Cornus は Compose と devcontainer を起点とし、自前のレジストリを同梱します。そして Docker、containerd、Kubernetes をまたいで `DeploySpec` を命令的に反映します。

## 関連ページ

- [Cornus とは](/ja/introduction/what-is-cornus) - 三つのサブシステムとエンドツーエンドの流れ。
- [クイックスタート](/ja/introduction/quick-start) - Compose ファイルから実行中のワークロードまで。
- [アーキテクチャ](/ja/architecture/) - 各要素の組み合わせと、その設計理由。
