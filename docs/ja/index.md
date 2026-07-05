---
layout: home

hero:
  name: Cornus
  text: Docker のワークフローを、そのまま Kubernetes で
  tagline: >-
    使い慣れた docker compose、docker CLI、devcontainer のワークロードを、そのまま Kubernetes 上に再現。
  image:
    src: /cornus-logo.svg
    alt: Cornus
  actions:
    - theme: brand
      text: クイックスタート
      link: /ja/introduction/quick-start
    - theme: alt
      text: Cornus とは
      link: /ja/introduction/what-is-cornus
    - theme: alt
      text: CLI リファレンス
      link: /ja/cli/
    - theme: alt
      text: GitHub で見る
      link: https://github.com/moriyoshi/cornus

features:
  - icon: 🔨
    title: ビルドエンジン + OCI レジストリ
    details: >-
      BuildKit のソルバーをバイナリに組み込んでいるため、別途 buildkitd を動かす必要は
      ありません。キャッシュ / シークレット / SSH マウント、名前付きコンテキスト、リモート
      キャッシュなど、docker buildx と同等の機能に対応します。ビルドしたイメージは組み込みの
      OCI Distribution v1.1 レジストリ (/v2/*) に格納され、永続化方式は差し替え可能です
      (ファイルシステム、インメモリ、S3、ビルドタグ経由の GCS / Azure Blob)。ビルドは
      ローカルでも、9P-on-WebSocket を介したリモートサーバー上でも実行できます。
    link: /ja/cli/build
    linkText: cornus build
  - icon: 🚀
    title: 命令的なデプロイエンジン
    details: >-
      dockerhost、ネイティブ containerd、デーモンレスの bare、client-go による Kubernetes という差し替え可能な
      デプロイバックエンドを、単一のインターフェースの背後に統合します。クライアント側の
      バインドマウント、ポートフォワード、下り (egress) 制御、ワークロード間をつなぐ hub
      オーバーレイにも対応します。
    link: /ja/reference/deploy-backends
    linkText: デプロイバックエンド
  - icon: 🔁
    title: ローカルブリッジの逆をいく
    details: >-
      Telepresence、mirrord、Gefyra は、プロセスを手元で動かしながらクラスター内にいるかの
      ように見せかけます。Cornus のアプローチはその逆です。実際のワークロードをクラスターへ
      デプロイし、クラスターのほうを手元に引き寄せます。公開ポートは 127.0.0.1 へ自動で
      フォワードされ、cornus exec / port-forward であらゆるコンテナポートに到達でき、
      *.cornus.internal は名前からサービスを解決します。
    link: /ja/introduction/comparison
    linkText: Cornus の違い
  - icon: 🐳
    title: Docker 互換のクライアント
    details: >-
      cornus compose は Docker Compose 互換のコマンドを提供します。cornus daemon docker は
      Docker Engine API のプロキシを公開するため、標準の docker CLI や devcontainer から
      リモートの Cornus サーバーを操作できます。devcontainer の定義もそのまま読み込めます。
    link: /ja/cli/compose
    linkText: cornus compose
  - icon: 🔐
    title: 既定で安全・リモート対応
    details: >-
      Bearer 認証 (静的トークン / JWT / JWKS)、mTLS による識別、識別子ごとの認可に対応します。
      いずれもオプトインで、無効時のオーバーヘッドはありません。接続プロファイルはクラスターへ
      自動でポートフォワードし、短命な認証情報を発行します。
    link: /ja/guides/security
    linkText: セキュリティと認証
  - icon: 📈
    title: 高いオブザーバビリティ
    details: >-
      OpenTelemetry のトレース・メトリクス・ログに加え、任意で Prometheus の /metrics
      エンドポイントを提供します。無効時のコストはなく、必要なときだけ有効化できます。
    link: /ja/architecture/
    linkText: アーキテクチャ
---
