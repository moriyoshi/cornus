import { Show, createSignal, createResource } from "solid-js";
import { getGraph, applyProject, type Project, type Workload, type Mount, type Tunnel } from "../api";
import DependencyGraph from "../components/DependencyGraph";
import WorkloadTable from "./Workloads";
import MountTable from "./Mounts";
import ForwardsView from "./Tunnels";

// slug turns a project title into a stable anchor id (used by the Overview cards
// to jump to a project section).
export function slug(title: string): string {
  return "project-" + title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "");
}

// ProjectSection is one project's slice of the dashboard: its workloads, mounts,
// and port-forwards, plus (for a real loaded project) the Apply control and the
// depends_on graph. Pass project=undefined for the ungrouped "Other" bucket —
// then the Apply/graph affordances are omitted.
export default function ProjectSection(props: {
  title: string;
  project?: Project;
  workloads: Workload[];
  mounts: Mount[];
  tunnels: Tunnel[];
  forwards: [string, string[]][];
  onChanged: () => void;
}) {
  const [applyOut, setApplyOut] = createSignal("");
  const [applying, setApplying] = createSignal(false);
  const [graph] = createResource(
    () => (props.project ? props.project.name : undefined),
    (name) => getGraph(name),
  );

  const apply = async () => {
    const p = props.project;
    if (!p) return;
    setApplying(true);
    setApplyOut("");
    try {
      await applyProject(p.name, (chunk) => setApplyOut((prev) => prev + chunk));
    } catch (e) {
      setApplyOut((prev) => prev + `\n${String(e)}`);
    } finally {
      setApplying(false);
    }
  };

  return (
    <section id={slug(props.title)} class="project">
      <div class="row">
        <h2 style={{ margin: 0 }}>{props.title}</h2>
        <Show when={props.project?.loaded}>
          <span class="badge">loaded</span>
          <button class="primary" disabled={applying()} onClick={() => void apply()}>
            {applying() ? "Applying…" : "Apply (up -d)"}
          </button>
        </Show>
      </div>

      <h3>Workloads</h3>
      <WorkloadTable workloads={props.workloads} onChanged={props.onChanged} />

      <h3>Mounts</h3>
      <MountTable mounts={props.mounts} />

      <h3>Port-forwards</h3>
      <ForwardsView tunnels={props.tunnels} forwards={props.forwards} />

      <Show when={applyOut()}>
        <h3>Apply output</h3>
        <pre class="log">{applyOut()}</pre>
      </Show>

      <Show when={graph() && graph()!.edges.length}>
        <h3>Dependency graph</h3>
        <DependencyGraph graph={graph()!} />
      </Show>
    </section>
  );
}
