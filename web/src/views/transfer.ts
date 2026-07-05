// The in-app file transfer, with no pane in it.
//
// A transfer is N items, named by virtual path, into one destination FOLDER, also named
// by a virtual path. Everything that decides what happens — the client-side refusals, the
// preflight, the overwrite question, the batch request, the outcome report — depends on
// those two things and on nothing else about who asked. This module is that half.
//
// It was FilePane.receiveInto, and it moved here when a TERMINAL pane became a legal
// destination: the BFF now reports each session's working directory, so "the folder this
// shell is standing in" is a virtual path like any other (see views/workspace/target.ts),
// and a terminal that ran its own copy of this would be a second set of answers to
// questions the drop path has already been taught — which items are already there, when an
// overwrite needs asking about, what a partial batch reads like.
//
// What stays with the caller is what is true of the DESTINATION VIEW rather than of the
// destination: ghost rows, the arrival flash and a listing to re-fetch are things a file
// pane has and a terminal does not. They are supplied as hooks, all optional.

import { createSignal } from "solid-js";
import {
  copyBatch,
  moveBatch,
  preflight,
  type FsBatchResult,
  type FsPreflightResult,
} from "../api";
import { confirmModal, promptChoice } from "../modal";
import { toast, toastError } from "../toast";

// The drag type carrying an in-app transfer. Virtual paths, so the receiving pane never
// needs to know which pane sent them — which is what lets a terminal receive at all.
export const DND_TYPE = "application/x-cornus-fs";

// DropItem is one entry in a transfer: its virtual path plus whether it is a directory. The
// receiving pane cannot stat what it is handed, and the kind is what the ghost rows need to
// draw the right placeholder. Named for the drag that first carried it; the cross-pane
// copy/move commands hand over the same list without a drag anywhere in sight.
export interface DropItem {
  path: string;
  dir: boolean;
}

// The folder an in-app drag started from, module-global because every pane that can
// RECEIVE one has to consult it: a drop that would put the items back where they already
// are is not offered at all — no highlight, no drop cursor — and `dragover` cannot ask the
// payload, since the dataTransfer is in protected mode there (it exposes the type list but
// none of the values). It lives here rather than in the file pane because a terminal pane
// standing in that same folder has to refuse for the same reason.
export const [dragOrigin, setDragOrigin] = createSignal<string | null>(null);

const baseOf = (p: string) => (p.includes("/") ? p.slice(p.lastIndexOf("/") + 1) : p);
const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

// formatBytes renders a transfer total, as a preflight reports it.
export function formatBytes(n: number): string {
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${u[i]}`;
}

// reportTransfer states the outcome of a batch: everything landed, or how much of it did
// and why the rest did not. The file pane wraps this to also re-fetch its listing, which a
// terminal has nothing to do.
export function reportTransfer(total: number, failed: string[], verb: string): void {
  if (failed.length) {
    const more = failed.length > 1 ? ` (+${failed.length - 1} more)` : "";
    toastError(`${verb} ${total - failed.length}/${total} — ${failed[0]}${more}`);
  } else {
    toast(`${verb} ${total} item${total === 1 ? "" : "s"}`);
  }
}

// askCopyOrMove is the question an EMULATED drop has to ask. For a mouse the answer is
// already in the gesture: Shift makes it a move, so the destructive one is the one you ask
// for. A FINGER HAS NO MODIFIER TO HOLD, and the alternatives all put the choice somewhere
// it cannot be seen — a second long press, a separate drop zone per verb, a mode set before
// the drag and forgotten during it. So an emulated drop asks, once it has landed somewhere
// legal and knows what it is asking about, and a dismissed question (null) is a drop that
// never happened.
export async function askCopyOrMove(destDir: string, items: DropItem[]): Promise<boolean | null> {
  const what = items.length === 1 ? `"${baseOf(items[0].path)}"` : `${items.length} items`;
  const answer = await promptChoice({
    title: `${what} → ${baseOf(destDir) || destDir}`,
    options: [
      { value: "copy", label: "Copy", glyph: "⧉" },
      { value: "move", label: "Move", glyph: "➜" },
    ],
  });
  return answer === null ? null : answer === "move";
}

// TransferSite is what only the destination's VIEW can do. A file pane showing the folder
// supplies all of it; a terminal pane supplies none, and the transfer is the same transfer.
export interface TransferSite {
  // admit is called with the items about to be attempted, BEFORE the preflight round trip:
  // a pane that draws ghost rows admits the transfer the instant it is asked for, rather
  // than after a network answer.
  admit?: (items: DropItem[]) => void;
  // wipe takes back what admit put up, by source path — an item the preflight refused or
  // the batch failed on was never a file here.
  wipe?: (paths: string[]) => void;
  // arrived names what landed, by basename, plus where it landed (which is not always the
  // folder the pane is showing).
  arrived?: (bases: string[], destDir: string) => void;
  // report states the outcome. Defaults to the toast; a pane with a listing also re-fetches.
  report?: (total: number, failed: string[], verb: string) => void;
  // refreshAll re-lists every open pane: a transfer changes two folders at once, and the
  // SOURCE is usually somebody else's pane.
  refreshAll?: () => void;
}

// transferInto runs one transfer: `incoming` into `destDir`, copying or moving.
//
// It does NOT check whether the destination is writable — that is the server's answer, and
// the preflight reports it as a refusal like any other. A pane that already knows its own
// mount is read-only may say so first (the file pane does), but only about ITS folder.
export async function transferInto(
  destDir: string,
  incoming: DropItem[],
  move: boolean,
  site: TransferSite = {},
): Promise<void> {
  let items = incoming;
  if (!items.length) return;
  const report = site.report ?? reportTransfer;
  const wipe = (paths: string[]) => site.wipe?.(paths);

  // Two refusals stay CLIENT-side, ahead of the preflight round trip. A folder put into
  // itself is decidable from the paths alone, and saying so instantly beats a network
  // round trip to be told the same thing. The OPERATION is the word both routes share, and
  // it distinguishes the two things a drop can be, which "drop" never did.
  if (items.some((i) => destDir === i.path || destDir.startsWith(`${i.path}/`))) {
    toastError(`cannot ${move ? "move" : "copy"} a folder into itself`);
    return;
  }
  items = items.filter((i) => joinPath(destDir, baseOf(i.path)) !== i.path); // already there
  if (!items.length) return;

  // Admission goes up BEFORE the preflight, not after it: its whole job is that the
  // destination admits the transfer the instant it is asked for, and deferring it until a
  // round trip answers would leave the drop looking like it did nothing. Anything the
  // preflight then refuses is wiped below — it was never a file.
  site.admit?.(items);

  // Everything else is asked of the server: a drop is one gesture with no confirmation
  // step, so a file about to be overwritten, an item that will be refused, or a transfer
  // too large for the relay used to be discoverable only by causing it.
  let plan: FsPreflightResult;
  try {
    plan = await preflight({ source: "virtual", path: destDir }, move ? "move" : "copy", destDir,
      items.map((i) => ({ from: i.path })));
  } catch (e) {
    wipe(items.map((i) => i.path));
    toastError(String(e));
    return;
  }

  const allowed = plan.items.filter((p) => p.action !== "refused");
  const refused = plan.items.filter((p) => p.action === "refused");
  wipe(refused.map((p) => p.from));
  // A refusal is reported through the SAME path as a failed transfer. The user does not
  // care whether the answer came before or during, and two vocabularies for one outcome
  // would be two things to learn.
  const refusals = refused.map((p) => `${baseOf(p.from)}: ${p.error ?? "refused"}`);
  if (!allowed.length) {
    report(items.length, refusals, move ? "moved" : "copied");
    return;
  }
  const going = allowed.map((p) => p.from);
  if (!(await confirmTransfer(plan, allowed.length, move))) {
    wipe(going);
    return;
  }
  // One request for the whole drop. Per-item results come back, so a partial transfer
  // still says exactly which rows landed.
  let result: FsBatchResult;
  try {
    const send = { source: "virtual" as const, path: destDir };
    const payload = going.map((p) => ({ from: p }));
    result = move ? await moveBatch(send, destDir, payload) : await copyBatch(send, destDir, payload);
  } catch (e) {
    wipe(going);
    toastError(String(e));
    return;
  }
  const ok = result.items.filter((r) => r.status === "ok").map((r) => r.from);
  const failed = [
    ...refusals,
    ...result.items.filter((r) => r.status === "failed").map((r) => `${baseOf(r.from)}: ${r.error ?? "failed"}`),
  ];
  wipe(going.filter((p) => !ok.includes(p)));
  report(items.length, failed, move ? "moved" : "copied");
  site.arrived?.(ok.map(baseOf), destDir);
  site.refreshAll?.(); // the SOURCE is usually a different pane
}

// confirmTransfer decides whether a transfer needs the user's word before it runs, and asks
// only when there is something to say. The common case — a clean transfer into empty
// space — stays a single gesture with no dialog, because a prompt every time is a prompt
// nobody reads.
async function confirmTransfer(
  plan: FsPreflightResult,
  allowed: number,
  move: boolean,
): Promise<boolean> {
  const refused = plan.items.filter((p) => p.action === "refused");
  const overwrites = plan.items.filter((p) => p.action === "overwrite");
  const merges = plan.items.filter((p) => p.action === "merge");
  const warnings = plan.items.flatMap((p) => p.warnings ?? []);
  if (!refused.length && !overwrites.length && !merges.length && !warnings.length) return true;

  const lines: string[] = [];
  if (overwrites.length) {
    lines.push(`${overwrites.length} existing item${overwrites.length === 1 ? "" : "s"} will be overwritten: ${
      overwrites.slice(0, 3).map((p) => baseOf(p.to)).join(", ")}${overwrites.length > 3 ? "…" : ""}`);
  }
  if (merges.length) {
    lines.push(`${merges.length} folder${merges.length === 1 ? "" : "s"} will be merged into, not replaced`);
  }
  if (refused.length) {
    lines.push(`${refused.length} will be skipped — ${refused[0].error}`);
  }
  // Warnings are deduplicated: a tree of oversize files would otherwise repeat one
  // sentence until the dialog is unreadable.
  for (const w of [...new Set(warnings)].slice(0, 3)) lines.push(w);
  if (plan.bytes > 0) lines.push(`${plan.files} file${plan.files === 1 ? "" : "s"}, ${formatBytes(plan.bytes)}`);

  return await confirmModal({
    title: `${move ? "Move" : "Copy"} ${allowed} item${allowed === 1 ? "" : "s"}?`,
    message: lines.join("\n"),
    confirmLabel: move ? "Move" : "Copy",
    danger: overwrites.length > 0,
  });
}
