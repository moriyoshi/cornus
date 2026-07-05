import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { Router, Route } from "@solidjs/router";
import { installMockFetch, setObsStore } from "../../mock/handler";
import { setUnservedMetrics } from "../../mock/metrics";
import Overview from "../Overview";
import WorkloadDetail from "../WorkloadDetail";
import MetricsStrip from "./MetricsStrip";
import { DEFAULT_RANGE } from "./catalog";

// The charts where a reader already is — inside a project section, inside a
// workload section, and on a workload's own page — rather than on the dashboard
// they would have to navigate to.

let restore: () => void;
beforeEach(() => {
  restore = installMockFetch();
  history.replaceState(null, "", "/");
});
afterEach(() => {
  cleanup();
  restore();
});

const renderOverview = () =>
  render(() => (
    <Router>
      <Route path="*" component={Overview} />
    </Router>
  ));

// renderDetail mounts the workload page at its real route, so useParams sees the
// name the way the app does.
function renderDetail(name: string) {
  history.replaceState(null, "", `/workloads/${encodeURIComponent(name)}`);
  return render(() => (
    <Router>
      <Route path="/workloads/:name" component={WorkloadDetail} />
    </Router>
  ));
}

// section finds a section by its heading and waits for its strip to settle.
async function section(heading: string): Promise<HTMLElement> {
  const h = await screen.findByRole("heading", { name: heading });
  const el = h.closest("section");
  if (!el) throw new Error(`no section around ${heading}`);
  return el as HTMLElement;
}

async function settledPanels(root: HTMLElement, count: number): Promise<HTMLElement[]> {
  await waitFor(() => {
    const done = root.querySelectorAll(
      "section.panel:has(svg.chart-plot), section.panel:has(.panel-table), section.panel:has(.chart-nodata)",
    );
    expect(done).toHaveLength(count);
  });
  return [...root.querySelectorAll<HTMLElement>("section.panel")];
}

describe("project section metrics", () => {
  it("charts the project's own workloads, and only those", async () => {
    renderOverview();
    const shop = await section("shop");
    const panels = await settledPanels(shop, 2);

    expect(panels.map((p) => p.querySelector("h3")!.textContent)).toEqual(["CPU", "Memory"]);

    // All four of the mock's replicas belong to the shop project, so all four are
    // here — including shop-worker's, whose deployment is "not created": the
    // STORE decides what has data, and a deployment that is stopped now still
    // has the hour behind it. Each is named with its workload, since the chart
    // holds more than one.
    const memory = panels[1];
    const legend = memory.querySelector("ul.chart-legend")!;
    expect(within(legend as HTMLElement).getAllByRole("listitem")).toHaveLength(4);
    expect(legend.textContent).toContain("shop-db · replica 0");
  });

  it("draws CPU from whichever spelling the backend records", async () => {
    // A server with no kubernetes workloads has never recorded
    // container_cpu_usage at all, so the panel's second source answers 400. That
    // is the NORMAL case for this panel — one of its two sources is unresolved on
    // every server — and it must not blank the chart that does have data.
    setUnservedMetrics(["container_cpu_usage"]);
    renderOverview();
    const shop = await section("shop");
    const [cpu] = await settledPanels(shop, 2);

    expect(cpu.querySelector(".panel-metric")!.textContent).toBe(
      "container_cpu_time / container_cpu_usage",
    );
    expect(cpu.querySelectorAll("path.chart-line").length).toBeGreaterThan(0);
    expect(cpu.querySelector("p.error")).toBeNull();
    expect(cpu.querySelector(".chart-nodata")).toBeNull();
    expect(cpu.querySelector(".panel-value")!.textContent).toMatch(/cores?$/);
  });

  it("carries no strip at all when the server has no store", async () => {
    // Not an explanatory box under every project heading: on this page the
    // absence of a store is not the reader's business.
    setObsStore(false);
    renderOverview();
    const shop = await section("shop");
    // Wait for the section to be fully rendered — its own Mounts heading, not
    // the one belonging to some other project section on the page.
    await waitFor(() => expect(within(shop).getByRole("heading", { name: "Mounts" })).toBeTruthy());
    expect(within(shop).queryByRole("heading", { name: "Metrics" })).toBeNull();
    expect(shop.querySelector("section.panel")).toBeNull();
  });
});

describe("workload section metrics", () => {
  it("charts one workload and links to its slice of the dashboard", async () => {
    renderOverview();
    fireEvent.click(screen.getByRole("tab", { name: "By workload" }));

    const web = await section("shop-web");
    const panels = await settledPanels(web, 2);
    const memory = panels[1];

    // shop-web has two replicas in the store and shop-db has one; only shop-web's
    // may appear under its own heading.
    const legend = memory.querySelector("ul.chart-legend")!;
    const rows = within(legend as HTMLElement).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    for (const r of rows) expect(r.textContent).not.toContain("shop-db");
    // Filtered to one workload, the legend stops repeating its name.
    expect(rows.map((r) => r.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("replica 0")]),
    );

    expect(within(web).getByRole("link", { name: /All metrics/ })).toHaveAttribute(
      "href",
      "/metrics?workload=shop-web",
    );
  });

  it("charts a stopped workload's history rather than pretending it has none", async () => {
    // shop-worker is "not created" right now. The store still holds what it did
    // before that, and the section shows it: what a deployment is doing NOW is
    // the status badge's job, not the chart's.
    renderOverview();
    fireEvent.click(screen.getByRole("tab", { name: "By workload" }));

    const worker = await section("shop-worker");
    const panels = await settledPanels(worker, 2);
    expect(panels[1].querySelectorAll("path.chart-line")).toHaveLength(1);
  });
});

describe("a scope spanning both deploy backends", () => {
  it("draws both CPU spellings on one chart, in one unit", async () => {
    // shop-db is host-backed and reports cumulative container_cpu_time;
    // edge-cache is kubernetes-backed and reports instantaneous
    // container_cpu_usage. They are the same quantity in the same unit, so they
    // belong on one axis — and a panel that read only its first source would
    // silently omit the kubernetes workload while looking complete.
    render(() => (
      <Router>
        <Route
          path="*"
          component={() => (
            <MetricsStrip services={["shop-db", "edge-cache"]} range={DEFAULT_RANGE} tick={0} />
          )}
        />
      </Router>
    ));
    const cpu = (await screen.findByRole("heading", { name: "CPU" })).closest(
      "section.panel",
    )! as HTMLElement;
    await waitFor(() => expect(cpu.querySelector("svg.chart-plot")).toBeTruthy());

    const legend = cpu.querySelector("ul.chart-legend")!;
    expect(legend.textContent).toContain("edge-cache");
    expect(legend.textContent).toContain("shop-db");
    // One axis, one unit: every series reads in cores.
    expect(cpu.querySelector(".panel-value")!.textContent).toMatch(/cores?$/);
  });
});

describe("an empty scope", () => {
  it("draws nothing at all, rather than the whole server", async () => {
    // An empty service set is a set with no members, not "everything" — a
    // project with no deployments must not show the rest of the server's traffic
    // under its heading. This is the one case no fixture can reach, because a
    // fixture project always has workloads.
    render(() => (
      <Router>
        <Route
          path="*"
          component={() => <MetricsStrip services={[]} range={DEFAULT_RANGE} tick={0} />}
        />
      </Router>
    ));
    expect(await screen.findByText(/Nothing deployed here to measure/)).toBeTruthy();
    expect(document.querySelector("section.panel")).toBeNull();
  });
});

describe("workload detail metrics section", () => {
  it("shows the full panel set for that workload alone", async () => {
    renderDetail("shop-db");

    const memory = (await screen.findByRole("heading", { name: "Memory in use" })).closest(
      "section.panel",
    )! as HTMLElement;
    await waitFor(() => expect(memory.querySelector("svg.chart-plot")).toBeTruthy());

    // The whole workload catalogue, not the two-panel strip.
    expect(screen.getByRole("heading", { name: "Network I/O" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Processes" })).toBeTruthy();
    // Server metrics belong to the dashboard, not to a workload's page.
    expect(screen.queryByRole("heading", { name: "Goroutines" })).toBeNull();

    // One replica, this workload's — a single series, so no legend box.
    expect(memory.querySelectorAll("path.chart-line")).toHaveLength(1);
    expect(memory.querySelector("ul.chart-legend")).toBeNull();
  });

  it("re-reads its own panels at the range picked in the section", async () => {
    const seen: string[] = [];
    const inner = globalThis.fetch;
    globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/observe/metrics")) seen.push(url);
      return inner(input, init);
    };
    try {
      renderDetail("shop-db");
      await waitFor(() => expect(seen.length).toBeGreaterThan(0));
      expect(seen.every((u) => u.includes("since=1h"))).toBe(true);

      seen.length = 0;
      fireEvent.change(screen.getByLabelText("Range"), { target: { value: "15m" } });
      await waitFor(() => expect(seen.length).toBeGreaterThan(0));
      expect(seen.every((u) => u.includes("since=15m") && u.includes("step=15s"))).toBe(true);
    } finally {
      globalThis.fetch = inner;
    }
  });

  it("explains the missing store instead of showing empty panels", async () => {
    setObsStore(false);
    renderDetail("shop-db");

    expect(await screen.findByRole("heading", { name: /No observability store/ })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Memory in use" })).toBeNull();
  });
});
