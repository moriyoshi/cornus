import { Show, For, createSignal } from "solid-js";
import {
  getConfig,
  getObsStatus,
  getWorkloads,
  getProjects,
  getMounts,
  getTunnels,
  getTerminals,
  type Workload,
  type Mount,
  type Tunnel,
} from "../api";
import { pollResource } from "../poll";
import ProjectSection from "./Projects";
import { WorkloadSection } from "./Workloads";
import { createMetricsClock } from "./metrics/clock";
import { DEFAULT_RANGE } from "./metrics/catalog";

type Grouping = "project" | "workload";

// Overview is the single, project-oriented dashboard: summary cards up top —
// server, workload counts and the live shells, client agent, conduit, i.e. the
// session-wide facts that hold no matter which project you are looking at — then
// one section per compose project, each carrying that project's workloads,
// mounts, and port-forwards (plus the depends_on graph). Workloads /
// mounts / forwards that don't belong to a loaded project fall into a trailing
// "Other" section, which closes the page.
export default function Overview() {
  const [config] = pollResource(getConfig, 10000);
  const [projects] = pollResource(getProjects, 5000);
  const [workloads, { refetch: refetchWorkloads }] = pollResource(getWorkloads);
  const [mounts] = pollResource(getMounts);
  const [tunnels] = pollResource(getTunnels, 5000);
  const [sessions] = pollResource(getTerminals, 2000);
  const [mode, setMode] = createSignal<Grouping>("project");

  // The section strips run on ONE window and ONE clock for the whole page: a
  // per-section range control would let two sections describe two different
  // moments while looking like one screen. The window is fixed here — the full
  // dashboard is where a reader changes it.
  const [obs] = pollResource(getObsStatus, 60000);
  const tick = createMetricsClock(() => DEFAULT_RANGE);
  // undefined until the answer is known, and while the store is absent: a server
  // without `--obs` should not grow an explanatory box under every heading, and a
  // strip that appears and then vanishes is worse than one that arrives late.
  //
  // Gated on HAVING a value rather than on `!loading`: this resource re-polls,
  // and `loading` is true during a refresh too, so a loading check would blink
  // every strip on the page off and on once a minute.
  const storeLive = () => obs.state === "ready" || obs.state === "refreshing";
  const metrics = () => (storeLive() ? { range: DEFAULT_RANGE, tick: tick() } : undefined);

  const runningCount = () => workloads()?.filter((w) => w.running).length ?? 0;
  const liveSessions = () => sessions()?.filter((s) => s.alive) ?? [];

  // Membership maps: a service or a tunnel's workload resolves to a project name.
  const wlByName = () => new Map((workloads() ?? []).map((w) => [w.name, w]));
  const serviceProject = () => {
    const m = new Map<string, string>();
    for (const w of workloads() ?? []) if (w.service && w.project) m.set(w.service, w.project);
    return m;
  };
  const tunnelProject = (t: Tunnel) => wlByName().get(t.workload)?.project;

  const known = () => new Set((projects() ?? []).map((p) => p.name));
  const inProject = (name: string) => (w: Workload | Mount) => w.project === name;
  const orphan = (p?: string) => !p || !known().has(p);

  const activeTunnels = () => (tunnels()?.tunnels ?? []).filter((t) => t.active);
  const forwardEntries = () => Object.entries(tunnels()?.forwards ?? {});

  // The ungrouped bucket: anything not attached to a known loaded project.
  const otherWorkloads = () => (workloads() ?? []).filter((w) => orphan(w.project));
  const otherMounts = () => (mounts() ?? []).filter((m) => orphan(m.project));
  const otherTunnels = () => activeTunnels().filter((t) => orphan(tunnelProject(t)));
  const otherForwards = () =>
    forwardEntries().filter(([svc]) => orphan(serviceProject().get(svc)));
  const hasOther = () =>
    otherWorkloads().length + otherMounts().length + otherTunnels().length + otherForwards().length >
    0;

  return (
    <>
      <h1>Overview</h1>
      <div class="cards">
        <div class="card">
          <h3>Server</h3>
          <Show when={config()} fallback={<span class="muted">loading…</span>}>
            {(c) => (
              <dl class="kv">
                <dt>Endpoint</dt>
                <dd>{c().endpoint}</dd>
                <Show when={c().context}>
                  <dt>Context</dt>
                  <dd>{c().context}</dd>
                </Show>
                <Show when={c().server?.registry_host}>
                  <dt>Registry</dt>
                  <dd>{c().server!.registry_host}</dd>
                </Show>
                {/* The deploy backend belongs here and only here: a server builds
                    one for its whole lifetime and stamps its name on every status
                    it reports, so it is a fact about this server — which is why no
                    workload table carries a column for it.

                    Rendered unconditionally, with "unknown" as the fallback. The
                    field is omitted when the server predates it OR could not
                    construct the backend to ask, and those are not "no backend" —
                    dropping the row would let a reader conclude the server has
                    none, which is the one thing that cannot be true. */}
                <dt>Backend</dt>
                <dd>
                  <Show
                    when={c().server?.backend}
                    fallback={
                      <span
                        class="muted"
                        title="This server did not report its deploy backend: it predates the field, or the backend could not be constructed to ask."
                      >
                        unknown
                      </span>
                    }
                  >
                    {(b) => (
                      <span class="row">
                        <span>{b().name || <span class="muted">unnamed</span>}</span>
                        {/* The one capability a backend self-describes, and it
                            decides whether a compose `depends_on: condition:
                            service_healthy` can EVER be satisfied here — a
                            planning fact, not trivia, so it earns a chip beside
                            the name rather than a line of its own. */}
                        <span
                          classList={{ badge: true, ok: b().reportsHealth }}
                          title={
                            b().reportsHealth
                              ? "This backend runs container health probes, so depends_on: condition: service_healthy can be satisfied."
                              : "This backend runs no container health probes, so depends_on: condition: service_healthy can never be satisfied here."
                          }
                        >
                          {b().reportsHealth ? "health probes" : "no health probes"}
                        </span>
                      </span>
                    )}
                  </Show>
                </dd>
                {/* Also backend-determined, and the detail that differs most between
                    them: kubernetes reports the cluster's own ingress, every other
                    backend has no controller to find and the server is the front
                    door itself. Absent (no row) means the server advertised nothing
                    — a host backend with no front door, or a cluster lookup that
                    failed — which is not a state worth asserting a label for. */}
                <Show when={c().server?.ingress}>
                  {(ing) => (
                    <>
                      <dt>Ingress</dt>
                      <dd>
                        <span class="row">
                          <span
                            class="badge"
                            title={
                              ing().emulated
                                ? "This server realizes ingress itself, routing to workloads with its own host/path table."
                                : "Ingress is realized by the cluster's own ingress controller."
                            }
                          >
                            {ing().emulated ? "emulated" : "cluster"}
                          </span>
                          {/* The base domain, and only that: it is what a reader
                              needs to predict the host a deploy will be given.
                              The ingress CLASS also rides on the wire but is an
                              operator's server default, not something to act on
                              — and a third item wraps this dd onto a second line
                              in a card whose value column is ~150px. */}
                          <Show when={ing().domain}>
                            <span class="muted">{ing().domain}</span>
                          </Show>
                        </span>
                      </dd>
                    </>
                  )}
                </Show>
                <dt>Status</dt>
                <dd>
                  <Show
                    when={!c().serverError}
                    fallback={<span class="badge bad">unreachable</span>}
                  >
                    <span class="badge ok">connected</span>
                  </Show>
                </dd>
                <dt>Version</dt>
                <dd>{c().version}</dd>
              </dl>
            )}
          </Show>
          <Show when={config()?.serverError}>
            <p class="error">{config()!.serverError}</p>
          </Show>
        </div>

        {/* The counts and the live shells are one subject: a terminal session IS
            a workload's, and the pair answers "what is deployed, and what am I
            currently inside of". Below the project sections it was a trailing
            table a reader had to scroll past everything to reach.

            The table keeps its `.table-scroll` wrapper and matters more here than
            anywhere else on the page: a card is the narrowest container in the
            app, so the four nowrap columns overflow it at every viewport, and the
            wrapper is what keeps that overflow inside the card. */}
        <div class="card">
          <h3>Workloads</h3>
          <p>
            {String(workloads()?.length ?? "…")} total, {runningCount()} running
          </p>
          <h4>Terminal sessions</h4>
          <Show
            when={liveSessions().length}
            fallback={<p class="muted">No live terminal sessions.</p>}
          >
            <div class="table-scroll">
              <table class="grid">
                <thead>
                  <tr>
                    <th>Workload</th>
                    <th>Agent</th>
                    {/* Title is what the session's foreground program calls itself
                        right now; Command is the argv it was launched with and
                        never changes. Both columns earn their place — the pair is
                        what tells you a tab labelled "vim" is a bash session. */}
                    <th>Title</th>
                    <th>Command</th>
                    <th>State</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={liveSessions()}>
                    {(s) => (
                      <tr>
                        <td>{s.workload}</td>
                        <td>{s.agent || <span class="muted">—</span>}</td>
                        <td class="wrap">{s.title || <span class="muted">—</span>}</td>
                        <td class="wrap">{s.cmd?.join(" ") || <span class="muted">—</span>}</td>
                        <td>
                          <Show when={s.state} fallback={<span class="muted">—</span>}>
                            <span
                              classList={{
                                badge: true,
                                warn: s.state === "blocked",
                                ok: s.state === "working",
                              }}
                            >
                              {s.state === "blocked" ? "needs you" : s.state}
                            </span>
                          </Show>
                        </td>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          </Show>
        </div>

        <div class="card">
          <h3>Client agent</h3>
          <Show when={config()} fallback={<span class="muted">loading…</span>}>
            {(c) => (
              <>
                <p>
                  <Show when={c().agentLive} fallback={<span class="badge">not running</span>}>
                    <span class="badge ok">live</span>
                  </Show>
                </p>
                <p class="muted">{c().agentSocket}</p>
              </>
            )}
          </Show>
        </div>

        {/* Conduit is how the local agent reaches workloads, so it belongs beside
            the agent's own card rather than in a stray heading below the project
            sections — where it read as a footnote to the last project rather than
            as a property of the whole session.

            Rendered unconditionally, like its neighbours: only the SOCKS5 conduit
            announces itself, so an empty banner list is the answer "no proxy", not
            "nothing to say". A card that disappears in that case leaves a reader
            unable to tell a proxy-less session from a dashboard that never had
            this card. */}
        <div class="card">
          <h3>Conduit</h3>
          <Show when={tunnels()} fallback={<span class="muted">loading…</span>}>
            {(t) => (
              <Show when={t().banners?.length} fallback={<p class="muted">No proxy conduit.</p>}>
                <For each={t().banners}>{(b) => <p class="muted">{b}</p>}</For>
              </Show>
            )}
          </Show>
        </div>
      </div>

      <div class="seg" role="tablist" aria-label="Overview grouping">
        <button
          role="tab"
          aria-selected={mode() === "project"}
          classList={{ active: mode() === "project" }}
          onClick={() => setMode("project")}
        >
          By project
        </button>
        <button
          role="tab"
          aria-selected={mode() === "workload"}
          classList={{ active: mode() === "workload" }}
          onClick={() => setMode("workload")}
        >
          By workload
        </button>
      </div>

      <Show
        when={mode() === "project"}
        fallback={
          <Show
            when={workloads()?.length}
            fallback={<p class="muted">No workloads.</p>}
          >
            <For each={workloads()}>
              {(w) => (
                <WorkloadSection
                  workload={w}
                  mounts={(mounts() ?? []).filter((m) => m.workload === w.name)}
                  tunnels={activeTunnels().filter((t) => t.workload === w.name)}
                  forwards={forwardEntries().filter(([svc]) => !!w.service && svc === w.service)}
                  onChanged={() => void refetchWorkloads()}
                  metrics={metrics()}
                />
              )}
            </For>
          </Show>
        }
      >
        <Show
          when={projects()?.length}
          fallback={<p class="muted">No compose projects loaded.</p>}
        >
          <For each={projects()}>
            {(p) => (
              <ProjectSection
                title={p.name}
                project={p}
                workloads={(workloads() ?? []).filter(inProject(p.name))}
                mounts={(mounts() ?? []).filter(inProject(p.name))}
                tunnels={activeTunnels().filter((t) => tunnelProject(t) === p.name)}
                forwards={forwardEntries().filter(([svc]) => serviceProject().get(svc) === p.name)}
                metrics={metrics()}
              />
            )}
          </For>
        </Show>

        <Show when={hasOther()}>
          <ProjectSection
            title="Other"
            workloads={otherWorkloads()}
            mounts={otherMounts()}
            tunnels={otherTunnels()}
            forwards={otherForwards()}
            metrics={metrics()}
          />
        </Show>
      </Show>

    </>
  );
}
