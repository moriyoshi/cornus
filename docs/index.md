---
layout: home

hero:
  name: Cornus
  text: Your Docker workflow, all the way to Kubernetes
  tagline: >-
    Take workloads from familiar docker compose, docker CLI, and devcontainers straight to
    Kubernetes.
  image:
    src: /cornus-logo.svg
    alt: Cornus
  actions:
    - theme: brand
      text: Quick start
      link: /introduction/quick-start
    - theme: alt
      text: What is Cornus?
      link: /introduction/what-is-cornus
    - theme: alt
      text: CLI reference
      link: /cli/
    - theme: alt
      text: View on GitHub
      link: https://github.com/moriyoshi/cornus

features:
  - icon: 🔨
    title: Build engine + OCI registry
    details: >-
      A BuildKit solver embedded in the binary — no separate buildkitd — with
      docker buildx parity (cache / secret / ssh mounts, named contexts, remote
      cache), feeding a built-in OCI Distribution v1.1 registry backed by a
      pluggable content store (filesystem, in-memory, S3, and behind a build tag
      GCS / Azure Blob). Builds run locally or on a remote server over
      9P-on-WebSocket.
    link: /cli/build
    linkText: cornus build
  - icon: 🚀
    title: Imperative deploy engine
    details: >-
      A pluggable deploy backend — dockerhost, native containerd, daemonless bare, and
      client-go Kubernetes — behind one interface, with client-local bind mounts, port
      forwarding, egress control, and a workload-to-workload hub overlay.
    link: /reference/deploy-backends
    linkText: Deploy backends
  - icon: 🔁
    title: The opposite of a local bridge
    details: >-
      Telepresence, mirrord, and Gefyra run your process on your laptop and fake
      it into the cluster. Cornus goes the other way: it deploys the real
      workload into the cluster and brings the cluster back to you — published
      ports auto-forward to 127.0.0.1, cornus exec / port-forward reach any
      container port, and *.cornus.internal resolves services by name.
    link: /introduction/comparison
    linkText: How Cornus differs
  - icon: 🐳
    title: Docker-compatible clients
    details: >-
      cornus compose speaks Docker Compose; cornus daemon docker exposes a
      Docker Engine API proxy so the stock docker CLI and devcontainers drive a
      remote Cornus server. Devcontainer definitions are read natively.
    link: /cli/compose
    linkText: cornus compose
  - icon: 🔐
    title: Secure & remote by default
    details: >-
      Bearer auth (static token / JWT / JWKS), mTLS identity, and per-identity
      authorization — all opt-in and zero-cost when off. Connection profiles
      auto-port-forward into a cluster and mint short-lived credentials.
    link: /topics/auth-and-tls
    linkText: Auth & TLS
  - icon: 📈
    title: Observable
    details: >-
      OpenTelemetry traces, metrics, and logs, plus an optional Prometheus
      /metrics endpoint — layered on and off without cost when disabled.
    link: /architecture/
    linkText: Architecture
---
