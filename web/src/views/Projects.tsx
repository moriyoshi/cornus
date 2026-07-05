import { Show, createResource } from "solid-js";
import { getGraph, type Project, type Workload, type Mount, type Tunnel } from "../api";
import DependencyGraph from "../components/DependencyGraph";
import MetricsStrip from "./metrics/MetricsStrip";
import type { Range } from "./metrics/catalog";
import WorkloadTable from "./Workloads";
import MountTable from "./Mounts";
import ForwardsView from "./Tunnels";

// slug turns a project title into a stable anchor id (used by the Overview cards
// to jump to a project section).
export function slug(title: string): string {
  return "project-" + title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "");
}

// ProjectSection is one project's slice of the dashboard: its workloads, mounts,
// and port-forwards, plus (for a real loaded project) the depends_on graph. Pass
// project=undefined for the ungrouped "Other" bucket — then the graph is omitted.
//
// No "Apply" button, and do not add one back. This UI is a companion to the CLI,
// not a second front door to it: re-deploying a project is `cornus compose up -d`,
// and the button was a browser affordance for re-execing exactly that. A companion
// shows you what the project is doing; the terminal you already have open is where
// you change it. Agents keep the operation through the `project_apply` MCP tool,
// which calls `webbff.Server.Apply` directly — they have no terminal to use instead.
export default function ProjectSection(props: {
  title: string;
  project?: Project;
  workloads: Workload[];
  mounts: Mount[];
  tunnels: Tunnel[];
  forwards: [string, string[]][];
  // metrics is the observability window the page is on, or undefined when the
  // server has no store — in which case the section carries no strip at all
  // rather than a box explaining a flag on every project heading.
  metrics?: { range: Range; tick: number };
}) {
  const [graph] = createResource(
    () => (props.project ? props.project.name : undefined),
    (name) => getGraph(name),
  );

  return (
    <section id={slug(props.title)} class="section">
      <div class="row">
        <h2 style={{ margin: 0 }}>{props.title}</h2>
        <Show when={props.project?.loaded}>
          <span class="badge">loaded</span>
        </Show>
      </div>

      <h3>Workloads</h3>
      <WorkloadTable workloads={props.workloads} />

      <Show when={props.metrics}>
        {(m) => (
          <MetricsStrip
            services={props.workloads.map((w) => w.name)}
            range={m().range}
            tick={m().tick}
            to="/metrics"
          />
        )}
      </Show>

      <h3>Mounts</h3>
      <MountTable mounts={props.mounts} scope="project" />

      <h3>Port-forwards</h3>
      <ForwardsView tunnels={props.tunnels} forwards={props.forwards} scope="project" />

      <Show when={graph() && graph()!.edges.length}>
        <h3>Dependency graph</h3>
        <DependencyGraph graph={graph()!} />
      </Show>
    </section>
  );
}
