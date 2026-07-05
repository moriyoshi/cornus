# クックブック

[ガイド](/ja/guides/)が一度に一つの機能の使い方を示すのに対し、このページ群では複数の機能を組み合わせ、実際の課題を解決するエンドツーエンドの手順を紹介します。正確なコマンド、完全なデプロイスペック、各要素のつながりを説明します。どの手順も、自分のプロジェクトに直接適用できるように書かれています。

## シナリオ

### [クライアントエグレスルーティングでコンテナ内 AI エージェントを実行する](/ja/cookbook/ai-agent-egress)

自律 AI エージェントをクラスター上のワークロードとして実行し、外向きの LLM API 呼び出しを自分のネットワーク (企業プロキシ / VPN / SASE) 経由にします。API キーは実行時に仲介するため、イメージには入りません。クライアント側の[エグレス](/ja/topics/egress)、資格情報ブローキング、[デプロイスペック](/ja/reference/deploy-spec)を組み合わせます。

### [クラスター上のリモート開発環境](/ja/cookbook/remote-dev-environment)

軽量なラップトップから高性能なリモートクラスターを使って開発します。ファイルはローカルで編集し、9P 経由でリモート実行し、`localhost` でポートへ接続し、通常の Docker / devcontainer ツールを操作できます。[Docker API プロキシ](/ja/cli/daemon)を通じて、VS Code や Zed で Dev Container を開くこともできます。[接続プロファイル](/ja/guides/remote-clusters)、[Compose / devcontainers](/ja/guides/compose-devcontainers-docker)、クライアントローカルバインドマウントを組み合わせます。

### [一時的なプレビュー環境](/ja/cookbook/preview-environments)

プル要求ごとにイメージをビルドして短命な環境を起動し、ホスト型トンネルで公開してレビュー担当者が URL を開けるようにします。終了も同じく迅速です。[ビルド](/ja/guides/building-images)、[デプロイ](/ja/guides/deploying-workloads)、[トンネル](/ja/guides/tunnels)を組み合わせます。

### [CI から Docker なしでビルドとデプロイを行う](/ja/cookbook/dockerless-ci)

どこにも Docker デーモンを置かずにビルドしてクラスターへ配信します。クラスター内ビルドエンジンがビルドし、同梱レジストリが保存し、containerd / Kubernetes がプルします。[ビルドエンジン](/ja/guides/building-images)、[レジストリ](/ja/guides/registry)、[デプロイバックエンド](/ja/reference/deploy-backends)を組み合わせます。

### [ローカル Compose プロジェクトをそのまま Kubernetes へ配信する](/ja/cookbook/compose-to-kubernetes)

動作している `compose.yaml` を、同じコマンド、同じファイルのまま実際の Kubernetes クラスターで実行します。マニフェストの書き換えは不要です。[Compose クライアント](/ja/guides/compose-devcontainers-docker)と[接続プロファイル](/ja/guides/remote-clusters)を組み合わせます。

### [hub オーバーレイでマイクロサービスを接続する](/ja/cookbook/microservices-hub)

独立してデプロイされたワークロード同士を、アドレスをハードコードせず、クラスター内またはバックエンドをまたいで安定した名前で到達可能にします。[hub オーバーレイ](/ja/topics/hub)と[デプロイスペック](/ja/reference/deploy-spec)を組み合わせます。
