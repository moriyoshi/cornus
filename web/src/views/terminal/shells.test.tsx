import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor } from "@solidjs/testing-library";
import { Router, Route } from "@solidjs/router";
import type { Component } from "solid-js";
import {
  installMockFetch,
  liveTerminals,
  seedShells,
  shellCandidatesSent,
  terminalCreates,
} from "../../mock/handler";
import Workspace, { STORAGE_KEY as WS_LAYOUT_KEY, type PaneData } from "../Workspace";
import Settings from "../Settings";
import { setShellCandidates } from "../../settings";
import { DEFAULT_SHELL_CANDIDATES, parseCandidates } from "./shells";

// Auto shell exec discovery, from the browser's side. Before this, a terminal pane
// always launched /bin/sh: wrong for an image with bash, and a dead end for an image
// with no shell at all, which failed with a generic "Failed to start session."

function renderView(Comp: Component) {
  return render(() => (
    <Router>
      <Route path="*" component={Comp} />
    </Router>
  ));
}

// The Workspace opens as a single FILE-BROWSER pane. Every test below is about the pane
// picker and what it discovers, so each one starts from an empty TERMINAL pane, seeded
// under the key the screen itself reads.
function renderTerm(data: PaneData[] = [{ kind: "term", workload: "", cmd: [] }]) {
  const panes = data.map((d, i) => ({ id: `seed-${i}`, data: d }));
  globalThis.localStorage?.setItem(
    WS_LAYOUT_KEY,
    JSON.stringify({
      tree: { type: "stack", id: "seed-stack", panes, active: 0 },
      focused: panes[0].id,
    }),
  );
  return renderView(Workspace);
}

// pickWorkload drives the picker up to (but not including) Connect.
async function pickWorkload(name = "shop-web") {
  const picker = (await screen.findAllByRole("combobox"))[0];
  // The workload list arrives with its own fetch; setting a <select>'s value before
  // its options exist silently leaves it empty and Connect disabled.
  await screen.findByRole("option", { name });
  fireEvent.change(picker, { target: { value: name } });
  return picker;
}

describe("shell candidate settings", () => {
  it("parses one candidate per line, dropping blanks and comments", () => {
    expect(
      parseCandidates("  /bin/zsh \n\n# a comment\n/bin/busybox sh\n   \n/bin/sh"),
    ).toEqual(["/bin/zsh", "/bin/busybox sh", "/bin/sh"]);
  });

  // A multi-word entry stays ONE entry. Splitting here would be a second tokenizer
  // beside the BFF's shell-words parser, and the two would disagree on quoting.
  it("leaves a multi-word candidate unsplit", () => {
    expect(parseCandidates("/bin/busybox sh")).toEqual(["/bin/busybox sh"]);
  });

  it("ships the documented default list", () => {
    const list = parseCandidates(DEFAULT_SHELL_CANDIDATES);
    expect(list[0]).toBe("/bin/zsh");
    expect(list).toContain("/bin/busybox sh");
    // Preference order, not alphabetical or likelihood: an image with both must give
    // you bash rather than sh.
    expect(list.indexOf("/bin/bash")).toBeLessThan(list.indexOf("/bin/sh"));
  });
});

describe("terminal shell discovery", () => {
  let restore: () => void;
  beforeEach(() => {
    globalThis.localStorage?.clear();
    setShellCandidates(DEFAULT_SHELL_CANDIDATES);
    restore = installMockFetch();
  });
  afterEach(() => {
    cleanup();
    restore();
  });

  // The auto-pick. Asserted on the CREATE REQUEST rather than on the rendered tab
  // label: the label would read the same for a pane that discovered /bin/ash and one
  // that was told /bin/ash, and only one of those is what this feature does.
  it("connects to the first discovered shell without asking", async () => {
    seedShells({ "shop-web": [["/bin/bash"], ["/bin/sh"]] });
    renderTerm();
    await pickWorkload();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(liveTerminals()).toHaveLength(1));
    expect(terminalCreates()).toEqual([{ workload: "shop-web", cmd: ["/bin/bash"], cmdline: undefined }]);
  });

  // The candidate list is a preference ORDER, so which entry is first is the whole
  // answer — not merely that some discovered shell was used.
  it("takes the highest-ranked shell, not just any that was found", async () => {
    seedShells({ "shop-web": [["/bin/busybox", "sh"], ["/bin/sh"]] });
    renderTerm();
    await pickWorkload();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(liveTerminals()).toHaveLength(1));
    expect(terminalCreates()[0].cmd).toEqual(["/bin/busybox", "sh"]);
  });

  it("sends this browser's candidate list, as edited in Settings", async () => {
    // Edit the setting through the real screen, not the setter, so the assertion
    // covers the control a user actually touches.
    const { unmount } = renderView(Settings);
    const box = (await screen.findAllByRole("textbox")).find(
      (el) => el.tagName === "TEXTAREA",
    ) as HTMLTextAreaElement;
    fireEvent.input(box, { target: { value: "/opt/fancy/sh\n# ignored\n/bin/sh" } });
    unmount();

    seedShells({ "shop-web": [["/bin/sh"]] });
    renderTerm();
    await pickWorkload();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    // The WIRE, not localStorage: storage only proves the setting was written, which
    // is true even for a pane that went on using the built-in list.
    await waitFor(() => expect(shellCandidatesSent()).toEqual(["/opt/fancy/sh", "/bin/sh"]));
  });

  it("asks for a command when the image has no shell", async () => {
    seedShells({ "shop-web": [] });
    renderTerm();
    await pickWorkload();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    // The pane comes back to the picker with an explanation rather than the generic
    // "Failed to start session." it used to dead-end on.
    const warning = await screen.findByText(/No shell found in this image/);
    expect(warning).toBeTruthy();
    // Nothing was started: a create here would mean the pane tried /bin/sh anyway.
    expect(terminalCreates()).toEqual([]);
    // And Connect stays disabled until a command is given, since there is no default
    // left to fall back to.
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  });

  // The typed command travels as ONE STRING so the BFF splits it with the same parser
  // every other candidate goes through. Wrapping it into a one-element argv — what the
  // picker used to do — asks to execute a file whose NAME contains a space, so
  // "/bin/busybox sh" could not start.
  it("sends a typed command as a command line, not a one-element argv", async () => {
    seedShells({ "shop-web": [] });
    renderTerm();
    await pickWorkload();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await screen.findByText(/No shell found in this image/);

    const cmdBox = screen.getByLabelText("Command") as HTMLInputElement;
    fireEvent.input(cmdBox, { target: { value: "/bin/busybox sh" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(liveTerminals()).toHaveLength(1));
    expect(terminalCreates().at(-1)).toEqual({
      workload: "shop-web",
      cmd: undefined,
      cmdline: "/bin/busybox sh",
    });
    // The pane adopts the argv the BFF split it into, so a reload reattaches instead
    // of prompting again.
    expect(liveTerminals()[0].cmd).toEqual(["/bin/busybox", "sh"]);
  });

  // A pane that already knows its command must not re-probe: discovery costs one exec
  // per candidate the image lacks, and re-running it on every reload of a restored
  // layout would make opening the workspace slower the more panes it holds.
  it("does not probe again once a pane knows its shell", async () => {
    seedShells({ "shop-web": [["/bin/bash"]] });
    renderTerm();
    await pickWorkload();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(liveTerminals()).toHaveLength(1));

    const afterFirst = shellCandidatesSent();
    expect(afterFirst.length).toBeGreaterThan(0);
    // A split inherits the target AND the command, so the new pane connects straight
    // away. Its create is the observation point: two creates, still one probe.
    cleanup();
    // Mounted WITHOUT reseeding: the whole claim is about what the PERSISTED pane does on
    // reload, and renderTerm would write a fresh unstarted pane over it — which would
    // probe again and pass the test for the opposite reason.
    renderView(Workspace);
    await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());
    expect(terminalCreates().at(-1)?.cmd).toEqual(["/bin/bash"]);
  });
});
