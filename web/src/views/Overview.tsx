import { Show, For, createSignal } from "solid-js";
import {
  getConfig,
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

type Grouping = "project" | "workload";

// Overview is the single, project-oriented dashboard: summary cards up top, then
// one section per compose project — each carrying that project's workloads,
// mounts, and port-forwards (plus Apply and the depends_on graph). Workloads /
// mounts / forwards that don't belong to a loaded project fall into a trailing
// "Other" section. Global bits (conduit banners, terminal sessions) close it out.
export default function Overview() {
  const [config] = pollResource(getConfig, 10000);
  const [projects] = pollResource(getProjects, 5000);
  const [workloads, { refetch: refetchWorkloads }] = pollResource(getWorkloads);
  const [mounts] = pollResource(getMounts);
  const [tunnels] = pollResource(getTunnels, 5000);
  const [sessions] = pollResource(getTerminals, 2000);
  const [mode, setMode] = createSignal<Grouping>("project");

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

        <div class="card">
          <h3>Workloads</h3>
          <p>
            {String(workloads()?.length ?? "…")} total, {runningCount()} running
          </p>
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
                onChanged={() => void refetchWorkloads()}
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
            onChanged={() => void refetchWorkloads()}
          />
        </Show>
      </Show>

      <Show when={tunnels()?.banners?.length}>
        <h2>Conduit</h2>
        <For each={tunnels()!.banners}>{(b) => <p class="muted">{b}</p>}</For>
      </Show>

      <h2>Terminal sessions</h2>
      <Show
        when={liveSessions().length}
        fallback={<p class="muted">No live terminal sessions.</p>}
      >
        <table class="grid">
          <thead>
            <tr>
              <th>Workload</th>
              <th>Agent</th>
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
      </Show>
    </>
  );
}
