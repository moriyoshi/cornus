// Canned BFF payloads for the /.cornus/web/* endpoints, shaped exactly like the
// webServer response structs in cmd/cornus/web.go. Shared by the component tests
// (a fetch stub, src/mock/handler.ts) and the standalone mock dev server
// (mock/server.ts), so the frontend can be developed and tested with zero
// backend. Model: a "shop" project — web depends_on db, redis; a bind mount and
// a named volume; one active tunnel.

import type {
  Config,
  Workload,
  WorkloadDetail,
  Project,
  Graph,
  Mount,
  TunnelsResponse,
  WebFile,
} from "../api";

export const config: Config = {
  endpoint: "http://localhost:5000",
  configPath: "/home/dev/.config/cornus/config.yaml",
  context: "default",
  contexts: ["default", "prod"],
  server: { registry_host: "localhost:5000", registry_scheme: "http" },
  project: "shop",
  composeFiles: ["compose.yaml"],
  agentSocket: "/run/user/1000/cornus/agent.sock",
  agentLive: true,
  version: "dev",
};

export const workloads: Workload[] = [
  {
    name: "shop-db",
    service: "db",
    project: "shop",
    image: "postgres:16",
    backend: "dockerhost",
    summary: "1/1 running",
    created: true,
    running: true,
    instances: [{ id: "db01", state: "running", running: true, health: "healthy" }],
  },
  {
    name: "shop-redis",
    service: "redis",
    project: "shop",
    image: "redis:7",
    backend: "dockerhost",
    summary: "1/1 running",
    created: true,
    running: true,
    instances: [{ id: "rd01", state: "running", running: true }],
  },
  {
    name: "shop-web",
    service: "web",
    project: "shop",
    image: "shop/web:latest",
    backend: "dockerhost",
    summary: "2/2 running",
    created: true,
    running: true,
    instances: [
      { id: "web01", state: "running", running: true, health: "healthy" },
      { id: "web02", state: "running", running: true, health: "healthy" },
    ],
    origin: {
      project: "shop",
      host: "laptop",
      user: "alice",
      directory: "/home/alice/src/shop",
      subject: "user:alice",
      git: {
        remote: "git@github.com:acme/shop.git",
        branch: "main",
        commit: "0123456789abcdef0123456789abcdef01234567",
        dirty: true,
      },
    },
  },
  {
    name: "shop-worker",
    service: "worker",
    project: "shop",
    image: "shop/worker:latest",
    summary: "not created",
    created: false,
    running: false,
  },
  {
    name: "legacy-cron",
    image: "busybox:latest",
    backend: "dockerhost",
    summary: "0/1 running",
    created: true,
    running: false,
    instances: [{ id: "cr01", state: "exited", running: false, exitCode: 0 }],
    // Deployed outside the loaded project: attributed to its recorded origin
    // project (lineage), not the loaded one.
    origin: { project: "ops", host: "cron-host", user: "root" },
  },
];

export const workloadDetails: Record<string, WorkloadDetail> = {
  "shop-web": {
    name: "shop-web",
    service: "web",
    project: "shop",
    status: {
      name: "shop-web",
      image: "shop/web:latest",
      backend: "dockerhost",
      instances: [
        { id: "web01", state: "running", running: true, health: "healthy" },
        { id: "web02", state: "running", running: true, health: "healthy" },
      ],
    },
    spec: {
      name: "shop-web",
      image: "shop/web:latest",
      replicas: 2,
      ports: [{ published: 8080, target: 80 }],
      env: ["NODE_ENV=production"],
      mounts: [{ source: "/srv/shop/html", target: "/usr/share/nginx/html", readOnly: true }],
    },
    tunnel: { active: true, url: "https://shop-demo.ngrok.app", port: 80 },
  },
};

export const projects: Project[] = [
  {
    name: "shop",
    services: ["db", "redis", "web", "worker"],
    running: ["db", "redis", "web"],
    loaded: true,
  },
];

export const graph: Graph = {
  project: "shop",
  nodes: [
    { service: "db", resource: "shop-db", image: "postgres:16", summary: "1/1 running", running: true, created: true },
    { service: "redis", resource: "shop-redis", image: "redis:7", summary: "1/1 running", running: true, created: true },
    { service: "web", resource: "shop-web", image: "shop/web:latest", summary: "2/2 running", running: true, created: true },
    { service: "worker", resource: "shop-worker", image: "shop/worker:latest", summary: "not created", running: false, created: false },
  ],
  edges: [
    { from: "web", to: "db", condition: "service_healthy", required: true },
    { from: "web", to: "redis", condition: "service_started", required: true },
    { from: "worker", to: "db", condition: "service_healthy", required: true },
  ],
};

export const mounts: Mount[] = [
  {
    project: "shop",
    service: "web",
    workload: "shop-web",
    kind: "bind",
    source: "/srv/shop/html",
    target: "/usr/share/nginx/html",
    readOnly: true,
    status: "live",
  },
  {
    project: "shop",
    service: "db",
    workload: "shop-db",
    kind: "volume",
    source: "shop_pgdata",
    target: "/var/lib/postgresql/data",
    status: "running",
  },
  {
    project: "shop",
    service: "worker",
    workload: "shop-worker",
    kind: "bind",
    source: "/srv/shop/jobs",
    target: "/jobs",
    status: "inactive",
  },
];

export const tunnels: TunnelsResponse = {
  tunnels: [
    { workload: "shop-web", active: true, url: "https://shop-demo.ngrok.app", port: 80 },
    { workload: "shop-db", active: false },
  ],
  forwards: { web: ["127.0.0.1:8080 -> :80"], db: ["127.0.0.1:5432 -> :5432"] },
  banners: ["SOCKS5 proxy at 127.0.0.1:1080"],
};

export const files: WebFile[] = [
  { path: "/srv/shop/compose.yaml", label: "compose.yaml", kind: "compose" },
  { path: "/home/dev/.config/cornus/config.yaml", label: "client config", kind: "clientconfig" },
];

export const fileContents: Record<string, string> = {
  "/srv/shop/compose.yaml":
    "services:\n  web:\n    image: shop/web:latest\n    depends_on:\n      db:\n        condition: service_healthy\n",
  "/home/dev/.config/cornus/config.yaml": '{\n  "current-context": "default"\n}\n',
};
