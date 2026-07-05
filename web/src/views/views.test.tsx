import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor } from "@solidjs/testing-library";
import { Router, Route } from "@solidjs/router";
import type { Component } from "solid-js";
import { installMockFetch } from "../mock/handler";
import Overview from "./Overview";
import Files from "./Files";
import Terminal from "./Terminal";
import Settings from "./Settings";
import { setPassBrowserShortcuts, setPrefixEnabled, setPrefix } from "../settings";
import { allCommands } from "../command-center";
import { choosePlacement } from "../pane-placement";
import { submitModal } from "../modal";

// runCommand invokes a contextual palette command by id (the Files workspace exposes
// its actions there instead of as on-screen buttons).
function runCommand(id: string) {
  const cmd = allCommands().find((c) => c.id === id);
  if (!cmd) throw new Error(`command not registered: ${id}`);
  cmd.run();
}

// These tests render the real Solid views against the mocked BFF (src/mock),
// proving the frontend turns /.cornus/web/* payloads into the expected DOM
// without any backend. Views use <A> and resources, so each is mounted inside a
// Router; findBy* waits out the mocked fetch's microtask resolution.

let restore: () => void;
beforeEach(() => {
  restore = installMockFetch();
});
afterEach(() => {
  cleanup();
  restore();
});

function renderView(Comp: Component) {
  return render(() => (
    <Router>
      <Route path="*" component={Comp} />
    </Router>
  ));
}

describe("Overview (project-oriented dashboard)", () => {
  it("groups workloads, mounts, and port-forwards under each project section", async () => {
    renderView(Overview);
    // Summary cards: server + project.
    expect(await screen.findByText("http://localhost:5000")).toBeInTheDocument();
    expect(await screen.findByText("connected")).toBeInTheDocument();
    // The loaded "shop" project renders its own section (anchored by slug), and the
    // project-less legacy-cron falls into the trailing "Other" section.
    expect(await screen.findByText("legacy-cron")).toBeInTheDocument();
    expect(document.getElementById("project-shop")).toBeTruthy();
    expect(document.getElementById("project-other")).toBeTruthy();
    // Under shop: its workloads (with per-row actions), its mounts, its forwards,
    // and the depends_on graph (two service_healthy edges). "shop-web" appears in
    // all three of the project's tables, so it is not a unique match.
    expect(await screen.findAllByText("shop-web")).not.toHaveLength(0);
    expect(await screen.findAllByRole("button", { name: "Restart" })).not.toHaveLength(0);
    expect(await screen.findByText("/usr/share/nginx/html")).toBeInTheDocument();
    expect(await screen.findByText("127.0.0.1:8080 -> :80")).toBeInTheDocument();
    expect(await screen.findByText("https://shop-demo.ngrok.app")).toBeInTheDocument();
    expect(await screen.findAllByText("healthy")).toHaveLength(2);
    // Global conduit banner still renders at the foot.
    expect(await screen.findByText("SOCKS5 proxy at 127.0.0.1:1080")).toBeInTheDocument();
  });

  it("scopes a project's workloads to that project (legacy-cron is not under shop)", async () => {
    renderView(Overview);
    const shop = (await screen.findByText("legacy-cron")) && document.getElementById("project-shop")!;
    // legacy-cron has no project, so it must not appear inside the shop section.
    expect(shop.textContent).not.toContain("legacy-cron");
    expect(shop.textContent).toContain("shop-web");
  });

  it("switches to a workload-oriented grouping via the toggle", async () => {
    renderView(Overview);
    // Wait for project-mode content backed by all three resources to settle.
    await screen.findByText("legacy-cron");
    await screen.findByText("https://shop-demo.ngrok.app");
    expect(document.getElementById("project-shop")).toBeTruthy();

    fireEvent.click(await screen.findByRole("tab", { name: "By workload" }));

    // Every workload now has its own section; the project sections are gone.
    expect(document.getElementById("workload-shop-web")).toBeTruthy();
    expect(document.getElementById("workload-legacy-cron")).toBeTruthy();
    expect(document.getElementById("project-shop")).toBeNull();
    // shop-web's section carries its own mount and public tunnel.
    const wl = document.getElementById("workload-shop-web")!;
    expect(wl.textContent).toContain("/usr/share/nginx/html");
    expect(wl.textContent).toContain("https://shop-demo.ngrok.app");
  });
});

describe("Files (explorer)", () => {
  // The tiled layout (splits + each pane's path) persists to localStorage; clear it
  // so each test starts from the default single pane at the virtual root.
  beforeEach(() => globalThis.localStorage?.clear());

  it("opens at a virtual root listing local roots and workloads as mounts", async () => {
    renderView(Files);
    // Local roots (by id) and workloads are the top-level mounts; a stopped
    // workload is flagged and not enterable.
    expect(await screen.findByText("project")).toBeInTheDocument();
    expect(await screen.findByText("assets")).toBeInTheDocument();
    expect(await screen.findByText("shop-web")).toBeInTheDocument();
    // Stopped workloads (shop-worker, legacy-cron) are flagged and not enterable.
    expect((await screen.findAllByText("(stopped)")).length).toBeGreaterThan(0);
  });

  it("browses a local mount and navigates into folders", async () => {
    renderView(Files);
    // Enter the project mount, then a folder inside it.
    fireEvent.click(await screen.findByText("project"));
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument();
    expect(await screen.findByText("README.md")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("web"));
    expect(await screen.findByText("nginx.conf")).toBeInTheDocument();
  });

  it("descends into a running workload's container filesystem", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("shop-web"));
    expect(await screen.findByText("hello.txt")).toBeInTheDocument();
  });

  it("shows the mount (workload/root) name in the tab label, like the terminal", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("shop-web")); // enter the workload mount
    await screen.findByText("hello.txt");
    expect(document.querySelector(".tab-label")?.textContent).toContain("shop-web");
    // Descend a folder → the tab keeps the mount alongside the current folder.
    fireEvent.click(await screen.findByText("etc"));
    await screen.findByText("nginx");
    expect(document.querySelector(".tab-label")?.textContent).toContain("shop-web");
  });

  it("opens an image file into a tiny image viewer pane", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("assets")); // the assets mount holds the logos
    expect(await screen.findByText("cornus-logo.svg")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("cornus-logo.png")); // mouse open → image viewer tab
    const img = await screen.findByRole("img");
    expect(img.getAttribute("src")).toContain("cornus-logo.png");
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
    // It is a viewer, not the editor — no Save control.
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("opens a text file by mouse click into a new editor tab (no prompt)", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("README.md")); // mouse open → stacks directly
    // The editor pane opened as a second tab, with no placement prompt shown.
    expect(await screen.findByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
    expect(document.querySelector(".modal-overlay")).toBeNull();
  });

  it("opens a text file via keyboard (Enter) through the placement prompt", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("README.md");
    // Select the row without opening it, then Enter on the list → prompt.
    const row = (await screen.findByText("README.md")).closest("tr")!;
    fireEvent.click(row);
    fireEvent.keyDown(document.querySelector(".fs-list")!, { key: "Enter" });
    choosePlacement("stack");
    expect(await screen.findByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  it("navigates rows with the arrow keys by moving the browser's focus", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    const list = document.querySelector(".fs-list")!;
    // Arrow down lands focus on the first row's link, and selection follows focus.
    fireEvent.keyDown(list, { key: "ArrowDown" });
    const first = document.activeElement as HTMLElement;
    expect(first.tagName).toBe("A");
    expect(first.closest(".fs-list")).toBeTruthy();
    expect(first.closest("tr")?.classList.contains("fs-selected")).toBe(true);
    // Arrow down again moves focus (and the single selection) to the next row.
    fireEvent.keyDown(list, { key: "ArrowDown" });
    const second = document.activeElement as HTMLElement;
    expect(second).not.toBe(first);
    expect(second.closest("tr")?.classList.contains("fs-selected")).toBe(true);
    expect(document.querySelectorAll(".fs-selected")).toHaveLength(1);
  });

  it("exposes actions as contextual palette commands, not a toolbar", async () => {
    renderView(Files);
    await screen.findByText("shop-web");
    // No on-screen action buttons; refresh moved onto the pane title bar.
    expect(screen.queryByRole("button", { name: "New folder" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Copy" })).toBeNull();
    expect(document.querySelector(".pane-refresh")).toBeTruthy();
    // At the root the mutation commands do not apply; entering a mount reveals them.
    expect(allCommands().some((c) => c.id === "files:new-folder")).toBe(false);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    expect(allCommands().some((c) => c.id === "files:new-folder")).toBe(true);
  });

  it("creates a new folder via a contextual palette command + text modal", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("project")); // focus a pane inside a mount
    await screen.findByText("compose.yaml");
    runCommand("files:new-folder"); // opens the text modal
    submitModal("brandnew");
    expect(await screen.findByText("brandnew")).toBeInTheDocument();
  });

  it("copies a file across mounts via a contextual palette command + text modal", async () => {
    renderView(Files);
    fireEvent.click(await screen.findByText("project"));
    // Select the row without opening it (clicking the name would open a new pane).
    const row = (await screen.findByText("README.md")).closest("tr")!;
    fireEvent.click(row);
    runCommand("files:copy"); // opens the copy destination modal
    submitModal("assets/README.md");
    // The copy landed under the assets mount (reached via the breadcrumb).
    fireEvent.click(await screen.findByText("All"));
    fireEvent.click(await screen.findByText("assets"));
    expect(await screen.findByText("README.md")).toBeInTheDocument();
  });

  it("splits a tile via an edge overlay into two explorer tiles", async () => {
    renderView(Files);
    await screen.findByText("shop-web"); // the initial pane's root listing rendered
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    fireEvent.click(await screen.findByRole("button", { name: "Split pane, new pane on the right" }));
    // A horizontal split with two independent tiles, each browsing the namespace.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelector(".split.h")).toBeTruthy();
    expect(await screen.findAllByText("shop-web")).toHaveLength(2);
  });

  it("closes a split tile via its tab, collapsing back to one", async () => {
    renderView(Files);
    await screen.findByText("shop-web");
    fireEvent.click(await screen.findByRole("button", { name: "Split pane, new pane on the bottom" }));
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    fireEvent.click(document.querySelectorAll<HTMLElement>(".tab-close")[0]);
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });
});

describe("Terminal workspace", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  it("starts with one empty pane and splits it via an edge overlay", async () => {
    renderView(Terminal);
    // Toolbar brand and a single empty-pane target picker (one combobox) render.
    expect(await screen.findByText("Terminal")).toBeInTheDocument();
    expect(screen.getAllByRole("combobox")).toHaveLength(1);
    // Each pane offers four edge-split overlays; clicking one yields a second
    // (empty) pane with its own picker.
    const rightEdge = await screen.findByRole("button", { name: "Split pane, new pane on the right" });
    fireEvent.click(rightEdge);
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
  });

  it("splits via the left edge too (new pane placed before the original)", async () => {
    renderView(Terminal);
    expect(screen.getAllByRole("combobox")).toHaveLength(1);
    const leftEdge = await screen.findByRole("button", { name: "Split pane, new pane on the left" });
    fireEvent.click(leftEdge);
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
  });

  it("stacks two panes into tabs when a tab is dragged onto another tile's center", async () => {
    renderView(Terminal);
    const rightEdge = await screen.findByRole("button", { name: "Split pane, new pane on the right" });
    fireEvent.click(rightEdge);
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
    // Two separate tiles, one tab each.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);

    const tabs = document.querySelectorAll(".tab");
    const stacks = document.querySelectorAll(".stack");
    // jsdom rects are zero → dropZone() returns "stack" (center) → stack the dragged
    // tab into the target tile.
    fireEvent.dragStart(tabs[0]);
    fireEvent.dragOver(stacks[1]);
    fireEvent.drop(stacks[1]);

    // Collapsed to a single tile carrying both panes as tabs.
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  it("moves (re-tiles) a pane when a tab is dropped on another tile's edge", async () => {
    renderView(Terminal);
    const rightEdge = await screen.findByRole("button", { name: "Split pane, new pane on the right" });
    fireEvent.click(rightEdge); // now a horizontal split of two tiles
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
    expect(document.querySelector(".split.h")).toBeTruthy();

    const tabs = document.querySelectorAll(".tab");
    const stacks = document.querySelectorAll<HTMLElement>(".stack");
    // Give the target tile a real rect and drop near its bottom edge → "bottom" → move.
    stacks[1].getBoundingClientRect = () =>
      ({ left: 0, top: 0, width: 100, height: 100, right: 100, bottom: 100, x: 0, y: 0, toJSON() {} }) as DOMRect;

    fireEvent.dragStart(tabs[0]);
    fireEvent.dragOver(stacks[1], { clientX: 50, clientY: 92 });
    fireEvent.drop(stacks[1], { clientX: 50, clientY: 92 });

    // Re-tiled: the split is now vertical, still two separate tiles.
    expect(document.querySelector(".split.v")).toBeTruthy();
    expect(document.querySelector(".split.h")).toBeNull();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("drags a whole tile by its tab bar onto another, merging all its tabs", async () => {
    renderView(Terminal);
    fireEvent.click(await screen.findByRole("button", { name: "Split pane, new pane on the right" }));
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    const bars = document.querySelectorAll(".stack-tabs");
    const stacks = document.querySelectorAll(".stack");
    // Drag the first tile's tab bar (the whole stack) onto the second tile's center.
    fireEvent.dragStart(bars[0]);
    fireEvent.dragOver(stacks[1]);
    fireEvent.drop(stacks[1]);
    // Both tiles collapsed into one carrying both panes as tabs.
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  it("focuses the new pane's workload picker when a pane is created", async () => {
    renderView(Terminal);
    const bottomEdge = await screen.findByRole("button", {
      name: "Split pane, new pane on the bottom",
    });
    fireEvent.click(bottomEdge);
    expect(screen.getAllByRole("combobox")).toHaveLength(2);
    // Focus lands on the workload picker of the freshly-created tile (marked .focused).
    const active = document.activeElement as HTMLElement;
    expect(active.tagName).toBe("SELECT");
    expect(active.closest(".stack")?.classList.contains("focused")).toBe(true);
  });

  it("splits, then closes a tab, collapsing back to one tile", async () => {
    renderView(Terminal);
    fireEvent.click(await screen.findByRole("button", { name: "Split pane, new pane on the right" }));
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    fireEvent.click(document.querySelectorAll<HTMLElement>(".tab-close")[0]);
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });

  it("New pane command prompts, then stacks the new pane as a tab", async () => {
    renderView(Terminal);
    await screen.findByText("Terminal");
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    runCommand("term:new"); // opens the placement prompt
    choosePlacement("stack");
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    expect(document.querySelectorAll(".stack")).toHaveLength(1); // one tile, two tabs
  });

  it("New pane command prompts, then splits into a new tile", async () => {
    renderView(Terminal);
    await screen.findByText("Terminal");
    runCommand("term:new");
    choosePlacement("split");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
  });
});

describe("Settings", () => {
  beforeEach(() => {
    globalThis.localStorage?.clear();
    // reset the global singleton between tests
    setPassBrowserShortcuts(false);
    setPrefixEnabled(true);
    setPrefix("Ctrl+Shift+X");
  });

  it("toggles the opt-in 'Pass browser shortcuts' setting and persists it globally", async () => {
    renderView(Settings);
    const toggle = (await screen.findByLabelText(/Pass browser shortcuts/)) as HTMLInputElement;
    expect(toggle.checked).toBe(false); // faithful terminal by default
    fireEvent.click(toggle);
    expect(toggle.checked).toBe(true);
    const saved = JSON.parse(globalThis.localStorage?.getItem("cornus.settings") || "{}");
    expect(saved.passBrowserShortcuts).toBe(true);
  });

  it("configures the prefix key and persists it", async () => {
    renderView(Settings);
    const combo = (await screen.findByLabelText("Prefix combination")) as HTMLSelectElement;
    expect(combo.value).toBe("Ctrl+Shift+X"); // tmux-safe default
    expect(combo.disabled).toBe(false); // enabled by default
    fireEvent.change(combo, { target: { value: "Ctrl+Shift+Space" } });
    const saved = JSON.parse(globalThis.localStorage?.getItem("cornus.settings") || "{}");
    expect(saved.prefix).toBe("Ctrl+Shift+Space");
  });
});
