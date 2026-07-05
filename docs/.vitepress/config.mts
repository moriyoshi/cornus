import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

type Lang = 'en' | 'ja' | 'zh'
type T = Record<Lang, string>

// A single trilingual navigation tree. Each entry carries its route (link) and
// the label in every supported language, so the sidebar/nav for a locale is
// derived by picking the language and prefixing the route with the locale base.
interface NavItem {
  link: string
  text: T
}
interface NavSection {
  text: T
  items: NavItem[]
}

const TREE: NavSection[] = [
  {
    text: { en: 'Introduction', ja: 'はじめに', zh: '简介' },
    items: [
      { link: '/introduction/what-is-cornus', text: { en: 'What is Cornus?', ja: 'Cornus とは', zh: 'Cornus 是什么？' } },
      { link: '/introduction/comparison', text: { en: 'Comparison with similar tools', ja: '類似ツールとの比較', zh: '与类似工具的比较' } },
      { link: '/introduction/installation', text: { en: 'Installation', ja: 'インストール', zh: '安装' } },
      { link: '/introduction/quick-start', text: { en: 'Quick start', ja: 'クイックスタート', zh: '快速开始' } },
    ],
  },
  {
    text: { en: 'Guides', ja: 'ガイド', zh: '指南' },
    items: [
      { link: '/guides/', text: { en: 'Guides', ja: 'ガイド', zh: '指南' } },
      { link: '/guides/building-images', text: { en: 'Building images', ja: 'イメージをビルドする', zh: '构建镜像' } },
      { link: '/guides/deploying-workloads', text: { en: 'Deploying workloads', ja: 'ワークロードをデプロイする', zh: '部署工作负载' } },
      { link: '/guides/compose-devcontainers-docker', text: { en: 'Compose, devcontainers, and the docker CLI', ja: 'Compose、Dev Container、Docker CLI', zh: 'Compose、devcontainer 和 docker CLI' } },
      { link: '/guides/remote-clusters', text: { en: 'Working with remote clusters', ja: 'リモートクラスターで作業する', zh: '使用远程集群' } },
      { link: '/guides/remote-docker-hosts', text: { en: 'Remote docker/containerd hosts over SSH', ja: 'SSH 経由のリモート docker/containerd ホスト', zh: '通过 SSH 访问远程 docker/containerd 主机' } },
      { link: '/guides/networking', text: { en: 'Networking and conduits', ja: 'ネットワークと conduit', zh: '网络与 conduit' } },
      { link: '/guides/hub', text: { en: 'The workload hub', ja: 'ワークロード間 hub', zh: '工作负载 Hub' } },
      { link: '/guides/tunnels', text: { en: 'Tunnels', ja: 'トンネル', zh: '隧道' } },
      { link: '/guides/ingress', text: { en: 'Ingress', ja: 'イングレス', zh: 'Ingress' } },
      { link: '/guides/egress', text: { en: 'Egress', ja: 'エグレス', zh: 'Egress' } },
      { link: '/guides/credentials', text: { en: 'Credentials', ja: '資格情報', zh: '凭据' } },
      { link: '/guides/registry', text: { en: 'Registry and storage', ja: 'レジストリとストレージ', zh: '镜像仓库和存储' } },
      { link: '/guides/security', text: { en: 'Security and authentication', ja: 'セキュリティと認証', zh: '安全与认证' } },
      { link: '/guides/observability', text: { en: 'Observability', ja: 'オブザーバビリティ', zh: '可观测性' } },
      { link: '/guides/output-modes', text: { en: 'Output modes', ja: '出力モード', zh: '输出模式' } },
    ],
  },
  {
    text: { en: 'Cookbook', ja: 'クックブック', zh: '实战手册' },
    items: [
      { link: '/cookbook/', text: { en: 'Cookbook', ja: 'クックブック', zh: 'Cookbook' } },
      { link: '/cookbook/ai-agent-egress', text: { en: 'Running an AI agent in a container with client egress routing', ja: 'クライアントエグレスルーティングでコンテナ内 AI エージェントを実行する', zh: '在容器中运行使用客户端 egress 路由的 AI agent' } },
      { link: '/cookbook/remote-dev-environment', text: { en: 'A remote development environment on a cluster', ja: 'クラスター上のリモート開発環境', zh: '集群上的远程开发环境' } },
      { link: '/cookbook/preview-environments', text: { en: 'Ephemeral preview environments', ja: '一時的なプレビュー環境', zh: '临时预览环境' } },
      { link: '/cookbook/dockerless-ci', text: { en: 'Docker-free build and deploy from CI', ja: 'CI から Docker なしでビルドとデプロイを行う', zh: '从 CI 无 Docker 地构建和部署' } },
      { link: '/cookbook/compose-to-kubernetes', text: { en: 'Shipping a local Compose project to Kubernetes unchanged', ja: 'ローカル Compose プロジェクトをそのまま Kubernetes へ配信する', zh: '不作修改地将本地 Compose 项目交付到 Kubernetes' } },
      { link: '/cookbook/microservices-hub', text: { en: 'Wiring microservices together over the hub overlay', ja: 'hub オーバーレイでマイクロサービスを接続する', zh: '通过 hub 覆盖网络连接微服务' } },
    ],
  },
  {
    text: { en: 'CLI reference', ja: 'CLI リファレンス', zh: 'CLI 参考' },
    items: [
      { link: '/cli/', text: { en: 'CLI reference', ja: 'CLI リファレンス', zh: 'CLI 参考' } },
      { link: '/cli/serve', text: { en: 'cornus serve', ja: 'cornus serve', zh: 'cornus serve' } },
      { link: '/cli/build', text: { en: 'cornus build', ja: 'cornus build', zh: 'cornus build' } },
      { link: '/cli/push', text: { en: 'cornus push', ja: 'cornus push', zh: 'cornus push' } },
      { link: '/cli/deploy', text: { en: 'cornus deploy', ja: 'cornus deploy', zh: 'cornus deploy' } },
      { link: '/cli/exec', text: { en: 'cornus exec', ja: 'cornus exec', zh: 'cornus exec' } },
      { link: '/cli/port-forward', text: { en: 'cornus port-forward', ja: 'cornus port-forward', zh: 'cornus port-forward' } },
      { link: '/cli/socks5', text: { en: 'cornus socks5', ja: 'cornus socks5', zh: 'cornus socks5' } },
      { link: '/cli/tunnel', text: { en: 'cornus tunnel', ja: 'cornus tunnel', zh: 'cornus tunnel' } },
      { link: '/cli/config', text: { en: 'cornus config', ja: 'cornus config', zh: 'cornus config' } },
      { link: '/cli/setup', text: { en: 'cornus setup', ja: 'cornus setup', zh: 'cornus setup' } },
      { link: '/cli/compose', text: { en: 'cornus compose', ja: 'cornus compose', zh: 'cornus compose' } },
      { link: '/cli/web', text: { en: 'cornus web', ja: 'cornus web', zh: 'cornus web' } },
      { link: '/cli/daemon', text: { en: 'cornus daemon', ja: 'cornus daemon', zh: 'cornus daemon' } },
      { link: '/cli/hub', text: { en: 'cornus hub', ja: 'cornus hub', zh: 'cornus hub' } },
      { link: '/cli/token', text: { en: 'cornus token', ja: 'cornus token', zh: 'cornus token' } },
      { link: '/cli/storage', text: { en: 'cornus storage', ja: 'cornus storage', zh: 'cornus storage' } },
      { link: '/cli/version-health', text: { en: 'cornus version / cornus health', ja: 'cornus version / cornus health', zh: 'cornus version / cornus health' } },
    ],
  },
  {
    text: { en: 'Reference', ja: 'リファレンス', zh: '参考' },
    items: [
      { link: '/reference/deploy-spec', text: { en: 'Deploy spec reference', ja: 'デプロイ仕様参照', zh: '部署规范参考' } },
      { link: '/reference/connection-config', text: { en: 'Connection config reference', ja: '接続設定参照', zh: '连接配置参考' } },
      { link: '/reference/server-env-vars', text: { en: 'Server environment variables', ja: 'サーバー環境変数', zh: '服务器环境变量' } },
      { link: '/reference/storage-backends', text: { en: 'Registry storage backends', ja: 'レジストリストレージバックエンド', zh: '镜像仓库存储后端' } },
      { link: '/reference/deploy-backends', text: { en: 'Deploy backends', ja: 'デプロイバックエンド', zh: '部署后端' } },
      { link: '/reference/helm-values', text: { en: 'Helm chart values', ja: 'Helm chart values', zh: 'Helm chart 值' } },
    ],
  },
  {
    text: { en: 'Architecture', ja: 'アーキテクチャ', zh: '架构' },
    items: [
      { link: '/architecture/', text: { en: 'Architecture overview', ja: 'アーキテクチャ概要', zh: '架构概览' } },
      { link: '/architecture/server-and-registry', text: { en: 'The server, registry, and content store', ja: 'サーバー、レジストリ、コンテンツストア', zh: '服务器、镜像仓库和内容存储' } },
      { link: '/architecture/build-engine', text: { en: 'The build engine and remote builds', ja: 'ビルドエンジンとリモートビルド', zh: '构建引擎和远程构建' } },
      { link: '/architecture/deploy-engine', text: { en: 'The deploy engine and backends', ja: 'デプロイエンジンとバックエンド', zh: '部署引擎和后端' } },
      { link: '/architecture/networking', text: { en: 'Networking: port forwarding, tunnels, ingress, and the hub', ja: 'ネットワーク: ポート転送、トンネル、イングレス、hub', zh: '网络：端口转发、tunnel、ingress 和 hub' } },
      { link: '/architecture/caretaker', text: { en: 'The caretaker and client-side features', ja: 'caretaker とクライアント側機能', zh: 'Caretaker 和客户端侧功能' } },
      { link: '/architecture/clients', text: { en: 'Docker-compatible clients and connection profiles', ja: 'Docker 互換クライアントと接続プロファイル', zh: '兼容 Docker 的客户端和连接配置文件' } },
      { link: '/architecture/security', text: { en: 'Security model', ja: 'セキュリティモデル', zh: '安全模型' } },
    ],
  },
]

// Route directory of each TREE section, in the same order. sidebarFor() indexes
// into this to decide which section is the reader's current one, so it must stay
// aligned with TREE.
const SECTIONS = ['introduction', 'guides', 'cookbook', 'cli', 'reference', 'architecture']

// Prefix a route with the locale base ('' for the root/English locale).
function withPrefix(prefix: string, link: string): string {
  return `${prefix}${link}`
}

// Build the sidebar for a language + locale prefix. Every section is listed so a
// reader can cross over, but only the one they are currently in is expanded --
// otherwise every page renders the whole site's link list.
function sidebarFor(lang: Lang, prefix: string, active?: string) {
  return TREE.map((section, i) => ({
    text: section.text[lang],
    collapsed: active === undefined ? false : SECTIONS[i] !== active,
    items: section.items.map((item) => ({
      text: item.text[lang],
      link: withPrefix(prefix, item.link),
    })),
  }))
}

// The top nav points at one representative page per section.
const NAV: { section: number; item: number }[] = [
  { section: 0, item: 0 }, // Introduction -> what-is-cornus
  { section: 1, item: 0 }, // Guides -> overview
  { section: 2, item: 0 }, // Cookbook -> overview
  { section: 3, item: 0 }, // CLI -> overview
  { section: 4, item: 0 }, // Reference -> deploy-spec
  { section: 5, item: 0 }, // Architecture -> overview
]

function navFor(lang: Lang, prefix: string) {
  return NAV.map(({ section, item }) => ({
    text: TREE[section].text[lang],
    link: withPrefix(prefix, TREE[section].items[item].link),
  }))
}

// Map every section prefix to a sidebar with that section expanded, for one locale.
function sidebarMap(lang: Lang, prefix: string) {
  const map: Record<string, ReturnType<typeof sidebarFor>> = {}
  for (const s of SECTIONS) {
    map[`${prefix}/${s}/`] = sidebarFor(lang, prefix, s)
  }
  return map
}

// https://vitepress.dev/reference/site-config
export default withMermaid(defineConfig({
  title: 'Cornus',
  description:
    'Bring your Docker workflow — docker compose, the docker CLI, and devcontainers — to Kubernetes or a plain Docker host, from a single Go binary.',

  // Project pages are served from https://cornus.dev/.
  base: '/',

  // Dead relative links fail the build — this is our primary link check.
  ignoreDeadLinks: false,

  // docs/README.md is a contributor readme browsed on GitHub (its ../ARCHITECTURE.md
  // link is repo-relative, not a site route), so keep it out of the built site.
  srcExclude: ['README.md'],

  lastUpdated: true,
  cleanUrls: true,

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/cornus-logo.svg' }],
  ],

  // Shared theme config; nav + sidebar are supplied per-locale below.
  themeConfig: {
    logo: '/cornus-logo.svg',

    search: {
      provider: 'local',
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/moriyoshi/cornus' },
    ],

    editLink: {
      pattern: 'https://github.com/moriyoshi/cornus/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },

    footer: {
      message: 'Released under the Apache-2.0 License.',
      copyright: 'Copyright © Moriyoshi Koizumi',
    },
  },

  locales: {
    root: {
      label: 'English',
      lang: 'en-US',
      themeConfig: {
        nav: navFor('en', ''),
        sidebar: sidebarMap('en', ''),
      },
    },
    ja: {
      label: '日本語',
      lang: 'ja-JP',
      link: '/ja/',
      themeConfig: {
        nav: navFor('ja', '/ja'),
        sidebar: sidebarMap('ja', '/ja'),
        editLink: {
          pattern: 'https://github.com/moriyoshi/cornus/edit/main/docs/:path',
          text: 'GitHub でこのページを編集',
        },
        docFooter: { prev: '前のページ', next: '次のページ' },
        outline: { label: '目次' },
        lastUpdatedText: '最終更新',
        returnToTopLabel: 'トップへ戻る',
        langMenuLabel: '言語を変更',
        darkModeSwitchLabel: '外観',
        sidebarMenuLabel: 'メニュー',
      },
    },
    zh: {
      label: '简体中文',
      lang: 'zh-CN',
      link: '/zh/',
      themeConfig: {
        nav: navFor('zh', '/zh'),
        sidebar: sidebarMap('zh', '/zh'),
        editLink: {
          pattern: 'https://github.com/moriyoshi/cornus/edit/main/docs/:path',
          text: '在 GitHub 上编辑此页',
        },
        docFooter: { prev: '上一页', next: '下一页' },
        outline: { label: '本页目录' },
        lastUpdatedText: '最后更新',
        returnToTopLabel: '返回顶部',
        langMenuLabel: '切换语言',
        darkModeSwitchLabel: '外观',
        sidebarMenuLabel: '菜单',
      },
    },
  },
}))
