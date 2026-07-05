import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor, within } from "@solidjs/testing-library";
import { Router, Route } from "@solidjs/router";
import { installMockFetch, setObsStore } from "../../mock/handler";
import { setUnservedMetrics } from "../../mock/metrics";
import Metrics from "../Metrics";

// The dashboard rendered against the mocked BFF, which answers the real store's
// shapes: generated series for the metrics cornus records, and the store's own
// 400 "not resolved" for a metric this backend never reports.

let restore: () => void;
beforeEach(() => {
  restore = installMockFetch();
  // The dashboard's filters live in the URL, and jsdom's location outlives a
  // test — so without this, one test's scope or workload leaks into the next and
  // the failure appears in whichever test happens to run after it.
  history.replaceState(null, "", "/");
});
afterEach(() => {
  cleanup();
  restore();
});

function renderMetrics(search = "") {
  if (search) history.replaceState(null, "", `/metrics${search}`);
  return render(() => (
    <Router>
      <Route path="*" component={Metrics} />
    </Router>
  ));
}

// panel finds a panel by its heading and returns the <section> around it, so an
// assertion is scoped to the panel it is about rather than to the whole page.
//
// It waits for the panel to SETTLE — a chart, a table, or a stated absence —
// because the heading renders immediately while the query behind it is still in
// flight, and an assertion made in that window passes or fails on timing.
async function panel(title: string): Promise<HTMLElement> {
  const heading = await screen.findByRole("heading", { name: title });
  const section = heading.closest("section.panel");
  if (!section) throw new Error(`no panel section around heading ${title}`);
  await waitFor(() =>
    expect(section.querySelector("svg.chart-plot, .panel-table, .chart-nodata")).toBeTruthy(),
  );
  return section as HTMLElement;
}

// bytesOf reads a rendered byte figure back into a number ("1.90 MiB/s" ->
// 1992294), so an assertion can be about the magnitude the reader sees rather
// than about which unit it happened to land in.
function bytesOf(text: string): number {
  const m = text.match(/([\d.]+)\s*(B|KiB|MiB|GiB|TiB)/);
  if (!m) throw new Error(`not a byte figure: ${text}`);
  return Number(m[1]) * 1024 ** ["B", "KiB", "MiB", "GiB", "TiB"].indexOf(m[2]);
}

describe("Metrics dashboard", () => {
  it("plots a recorded metric, one line per replica, with the values in the legend", async () => {
    renderMetrics();
    const memory = await panel("Memory in use");

    // The chart is drawn: one path per series. Five, not four — the modelled
    // server also runs a kubernetes-backed `edge-cache`, and an unfiltered
    // dashboard shows every deployment the STORE knows, not only the ones the
    // loaded compose project lists.
    const svg = memory.querySelector("svg.chart-plot")!;
    expect(svg).toBeTruthy();
    expect(svg.querySelectorAll("path.chart-line")).toHaveLength(5);

    // Every series' latest value is in the legend, so no number on this chart is
    // reachable only by hovering.
    const legend = memory.querySelector("ul.chart-legend")!;
    const rows = within(legend as HTMLElement).getAllByRole("listitem");
    expect(rows).toHaveLength(5);
    expect(rows.map((r) => r.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("shop-web · replica 0")]),
    );
    for (const r of rows) expect(r.textContent).toMatch(/MiB|KiB|B$/);

    // And the panel leads with the current total across those series.
    expect(memory.querySelector(".panel-value")!.textContent).toMatch(/MiB|GiB/);
  });

  it("differentiates a cumulative counter instead of drawing it climbing", async () => {
    renderMetrics();
    const net = await panel("Network I/O");

    // container_network_io is CUMULATIVE bytes. The panel must read a rate, and
    // the assertion has to be on the MAGNITUDE rather than on the unit string:
    // the undifferentiated counter formats identically ("… /s") while being the
    // hour's whole total, three orders of magnitude wrong. The mock's replicas
    // each move a few hundred KiB/s, so the sum is a couple of MiB/s; the raw
    // counter over the same window would read in GiB.
    const value = net.querySelector(".panel-value")!.textContent!;
    expect(value).toMatch(/\/s$/);
    expect(bytesOf(value)).toBeLessThan(100 * 1024 ** 2);
    expect(bytesOf(value)).toBeGreaterThan(0);

    // receive and transmit for each of the four replicas — exactly the palette's
    // eight, so all are drawn and none is withheld.
    expect(net.querySelectorAll("path.chart-line")).toHaveLength(8);
    expect(net.querySelector(".panel-capped")).toBeNull();
  });

  it("reads a metric nothing has reported as no data, not as an error", async () => {
    // A server with no kubernetes-backed workload has never recorded
    // container_cpu_usage, and the store rejects the NAME with a 400 diagnostic
    // rather than answering with an empty result. That is not a failure to
    // report — it is the honest "no data" for a metric this backend never emits.
    setUnservedMetrics(["container_cpu_usage"]);
    renderMetrics();
    const cpu = await panel("CPU (instantaneous)");
    expect(await within(cpu).findByText(/Nothing has reported container_cpu_usage yet/)).toBeTruthy();
    expect(cpu.querySelector("p.error")).toBeNull();
  });

  it("offers every chart as a table of numbers", async () => {
    renderMetrics();
    const memory = await panel("Memory in use");
    fireEvent.click(within(memory).getByRole("button", { name: "Table" }));

    const table = await within(memory).findByRole("table");
    expect(within(table).getByRole("columnheader", { name: "Last" })).toBeTruthy();
    // One row per series, each carrying the four numbers the chart positions.
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(5);
    for (const r of rows) expect(within(r).getAllByRole("cell")).toHaveLength(5);
    expect(memory.querySelector("svg.chart-plot")).toBeNull();
  });

  it("answers the keyboard with the same readout as the pointer", async () => {
    renderMetrics();
    const memory = await panel("Memory in use");
    const svg = memory.querySelector("svg.chart-plot")! as SVGSVGElement;

    // Nothing is quoted until the reader asks.
    expect(memory.querySelector(".chart-tip")).toBeNull();
    fireEvent.keyDown(svg, { key: "ArrowRight" });

    const tip = memory.querySelector(".chart-tip")!;
    expect(tip).toBeTruthy();
    // Every series at that instant, values first.
    expect(tip.querySelectorAll(".chart-tip-row")).toHaveLength(5);
    expect(tip.querySelector(".chart-tip-value")!.textContent).toMatch(/MiB|KiB|B/);
    expect(tip.querySelector(".chart-tip-time")!.textContent).toMatch(/^\d{2}:\d{2}/);

    fireEvent.keyDown(svg, { key: "Escape" });
    expect(memory.querySelector(".chart-tip")).toBeNull();
  });

  it("filters every panel to one workload from the one filter row", async () => {
    renderMetrics();
    const memory = await panel("Memory in use");
    expect(memory.querySelectorAll("path.chart-line")).toHaveLength(5);

    fireEvent.change(screen.getByLabelText("Workload"), { target: { value: "shop-db" } });

    // The filter is applied to the series already fetched — one deployment, one
    // replica — and the legend stops repeating the workload name.
    const filtered = await panel("Memory in use");
    expect(filtered.querySelectorAll("path.chart-line")).toHaveLength(1);
    // A single series needs no legend box: the panel heading names it.
    expect(filtered.querySelector("ul.chart-legend")).toBeNull();
    expect(filtered.querySelector("path.chart-area")).toBeTruthy();
  });

  // The scope switch decides whether the workload filter exists at all, so it
  // cannot be that filter's peer in one row — a reader could not tell, from a row
  // of three equal-looking controls, why one of them comes and goes. Asserted by
  // CONTAINMENT and by document order, not by "the Server button is on the page":
  // the old markup passed that unchanged, which is the whole point.
  it("puts the scope switch with the page title, above the filters it governs", async () => {
    renderMetrics();
    await panel("Memory in use");

    const seg = screen.getByRole("group", { name: "Scope" });
    const head = seg.closest(".page-head") as HTMLElement | null;
    expect(head).toBeTruthy();
    expect(within(head!).getByRole("heading", { name: "Metrics", level: 1 })).toBeInTheDocument();

    const filters = document.querySelector(".filters") as HTMLElement;
    expect(filters.contains(seg)).toBe(false);
    // The control it governs is still in the row, so this is a MOVE of the driver
    // out, not a move of everything up.
    expect(within(filters).getByLabelText("Workload")).toBeInTheDocument();
    expect(within(filters).getByLabelText("Range")).toBeInTheDocument();
    // Driver before driven in reading order, not merely in a different container.
    expect(head!.compareDocumentPosition(filters) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  // A switch between two equally empty screens is a choice with no consequence.
  // The title stays, so the page is still identifiable while it explains itself.
  it("offers no scope switch when there is no store to scope", async () => {
    setObsStore(false);
    renderMetrics();
    expect(
      await screen.findByRole("heading", { name: "No observability store on this server" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Scope" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Metrics", level: 1 })).toBeInTheDocument();
  });

  it("switches to the server's own metrics, which carry no workload filter", async () => {
    renderMetrics();
    await panel("Memory in use");
    expect(screen.getByLabelText("Workload")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Server" }));

    expect(await panel("Goroutines")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Memory in use" })).toBeNull();
    expect(screen.queryByLabelText("Workload")).toBeNull();
  });

  it("re-reads at the range the reader picked", async () => {
    const seen: string[] = [];
    const restoreInner = installMockFetch();
    const inner = globalThis.fetch;
    globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/observe/metrics")) seen.push(url);
      return inner(input, init);
    };
    try {
      renderMetrics();
      await panel("Memory in use");
      expect(seen.every((u) => u.includes("since=1h") && u.includes("step=1m"))).toBe(true);

      seen.length = 0;
      fireEvent.change(screen.getByLabelText("Range"), { target: { value: "24h" } });
      await panel("Memory in use");
      expect(seen.length).toBeGreaterThan(0);
      expect(seen.every((u) => u.includes("since=24h") && u.includes("step=15m"))).toBe(true);
    } finally {
      restoreInner();
    }
  });

  it("arrives already scoped when the URL says so", async () => {
    // This is the contract behind the "All metrics →" links in the project and
    // workload sections: the scope travels in the URL, so the link lands on the
    // same slice the section was showing — and the reader can share it.
    renderMetrics("?workload=shop-db&range=24h");
    const memory = await panel("Memory in use");

    expect((screen.getByLabelText("Workload") as HTMLSelectElement).value).toBe("shop-db");
    expect((screen.getByLabelText("Range") as HTMLSelectElement).value).toBe("24h");
    expect(memory.querySelectorAll("path.chart-line")).toHaveLength(1);
  });

  it("folds a hand-edited range back to the default rather than emptying the page", async () => {
    renderMetrics("?range=nonsense");
    expect((screen.getByLabelText("Range") as HTMLSelectElement).value).toBe("1h");
    expect(await panel("Memory in use")).toBeTruthy();
  });

  it("explains a server with no store instead of reporting an error", async () => {
    setObsStore(false);
    renderMetrics();

    expect(await screen.findByRole("heading", { name: /No observability store/ })).toBeTruthy();
    expect(screen.getByText("cornus serve --obs")).toBeTruthy();
    // No panels, no filter row, and nothing painted red: this is a setting.
    expect(screen.queryByRole("heading", { name: "Memory in use" })).toBeNull();
    expect(screen.queryByLabelText("Range")).toBeNull();
    expect(document.querySelector("p.error")).toBeNull();
  });
});
