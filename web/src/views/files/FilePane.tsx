import { For, Show, createResource, createSignal, onCleanup, onMount } from "solid-js";
import {
  copyPath,
  deletePath,
  fsContentURL,
  listDir,
  mkdir,
  renamePath,
  uploadFile,
  type FsEntry,
  type FsLocation,
} from "../../api";
import type { Pane } from "../tiling/layout";
import { promptText, confirmModal } from "../../modal";

// FileData is a Files pane's durable payload. A BROWSER pane has just `path` (a
// directory in the virtual namespace). An EDITOR pane also has `open` set (the filename
// within `path` shown in the editor). Kept on the pane node so it travels through
// splits, moves, stacking, and localStorage persistence.
export interface FileData {
  path: string;
  open?: string;
}

// PaneActions is the focused-pane surface the workspace exposes to the command palette
// (and the sub-header refresh/save). It is a discriminated union: a browsing pane offers
// filesystem mutations; an editing pane offers save. The reactive getters let a
// contextual command decide whether it applies right now.
export interface BrowseActions {
  kind: "browse";
  atRoot: () => boolean;
  go: (path: string) => void;
  selected: () => string | undefined;
  refresh: () => void;
  newFolder: () => void;
  upload: () => void;
  rename: () => void;
  copy: () => void;
  remove: () => void;
  download: () => void;
}
export interface EditActions {
  kind: "edit";
  go: (path: string) => void;
  refresh: () => void;
  dirty: () => boolean;
  save: () => void;
}
export type PaneActions = BrowseActions | EditActions;

// FilePane is one independent file-explorer pane: a Finder-style browser over the
// unified virtual namespace whose root lists the mounts (local roots + workloads). Its
// location lives in the pane payload; navigation calls props.navigate. Opening a text
// file calls props.openFile, which the workspace turns into a new editor pane.

const dirOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
const baseOf = (p: string) => (p.includes("/") ? p.slice(p.lastIndexOf("/") + 1) : p);
const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

function formatSize(e: FsEntry): string {
  if (e.kind === "dir") return "—";
  const u = ["B", "KB", "MB", "GB"];
  let n = e.size;
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${u[i]}`;
}

const icon = (e: FsEntry) => (e.kind === "dir" ? "📁" : e.kind === "symlink" ? "🔗" : "📄");

// isTextName is a best-effort guess for whether a file opens in the editor (vs
// downloads).
export function isTextName(name: string): boolean {
  return /\.(txt|md|ya?ml|json|toml|ini|conf|cfg|env|sh|ts|tsx|js|jsx|css|html?|xml|go|py|rs|sql|log|dockerfile|gitignore)$/i.test(
    name,
  ) || !name.includes(".") || /^\./.test(name);
}

// isImageName reports whether a file opens in the image viewer.
export function isImageName(name: string): boolean {
  return /\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i.test(name);
}

export default function FilePane(props: {
  pane: Pane<FileData>;
  navigate: (path: string) => void;
  // openFile asks the workspace to open a text file (in this pane's directory) as a new
  // editor pane. fromKeyboard requests the placement prompt (stack vs split); a mouse
  // open uses a default disposition without prompting.
  openFile: (name: string, fromKeyboard: boolean) => void;
  // register publishes this pane's actions to the workspace while it is mounted.
  register: (actions: PaneActions) => () => void;
}) {
  const path = () => props.pane.data.path;
  const loc = (): FsLocation => ({ source: "virtual", path: path() });
  const atRoot = () => path() === "";

  const [selected, setSelected] = createSignal<string>();
  const [status, setStatus] = createSignal("");

  // Keyed on an object (never falsy) so the virtual root (path "") still fetches.
  const [listing, { refetch }] = createResource(
    () => ({ path: path() }),
    (src) => listDir({ source: "virtual", path: src.path }),
  );

  const entries = () => listing()?.entries ?? [];

  // Each row's name link registers itself here so keyboard navigation can move the
  // browser's focus onto a sibling row (arrow keys) — real DOM focus, not just a
  // highlight, so the focus ring, Tab order, and command selection all track together.
  const rowRefs = new Map<string, HTMLAnchorElement>();

  // Roving tabindex: exactly one row is in the tab order at a time — the selected row,
  // or the first when nothing is selected yet — so Tab enters the list on that row and
  // then leaves it (arrows move within), instead of stepping through every file.
  const rovingName = () => {
    const list = entries();
    const sel = selected();
    return sel && list.some((e) => e.name === sel) ? sel : list[0]?.name;
  };

  // focusRow moves the browser's focus onto a row's name link (which selects it via
  // onFocus); used by mouse selection and arrow-key navigation.
  const focusRow = (name: string) => rowRefs.get(name)?.focus();

  // go moves this pane to a new virtual path (persisted in the tree via navigate).
  const go = (nextPath: string) => {
    setStatus("");
    setSelected(undefined);
    rowRefs.clear();
    props.navigate(nextPath);
  };

  const childPath = (name: string) => joinPath(path(), name);
  const childLoc = (name: string): FsLocation => ({ source: "virtual", path: childPath(name) });

  const enterDir = (name: string) => go(childPath(name));

  const goUp = () => {
    if (!path()) return;
    go(dirOf(path()));
  };

  // Activate a row: descend into dirs (blocking stopped workload mounts), open text
  // files as a new editor pane (prompting for placement only when keyboard-induced),
  // download the rest.
  const activate = (e: FsEntry, fromKeyboard = false) => {
    if (e.running === false) {
      setStatus(`${e.name} is not running`);
      return;
    }
    if (e.kind === "dir") enterDir(e.name);
    else if (isTextName(e.name) || isImageName(e.name)) props.openFile(e.name, fromKeyboard);
    else download(e.name);
  };

  const newFolder = async () => {
    const name = (await promptText({ title: "New folder", label: "Folder name", confirmLabel: "Create" }))?.trim();
    if (!name) return;
    try {
      await mkdir(childLoc(name));
      setStatus(`created ${name}`);
      void refetch();
    } catch (e) {
      setStatus(String(e));
    }
  };

  let fileInput!: HTMLInputElement;
  const onUpload = async (files: FileList | null) => {
    if (!files || !files.length) return;
    try {
      for (const f of Array.from(files)) await uploadFile(loc(), f);
      setStatus(`uploaded ${files.length} file(s)`);
      void refetch();
    } catch (e) {
      setStatus(String(e));
    } finally {
      fileInput.value = "";
    }
  };

  const rename = async () => {
    const name = selected();
    if (!name) return;
    const next = (
      await promptText({ title: "Rename", label: "New name", initial: name, confirmLabel: "Rename" })
    )?.trim();
    if (!next || next === name) return;
    try {
      await renamePath(childLoc(name), joinPath(path(), baseOf(next)));
      setSelected(next);
      setStatus(`renamed to ${next}`);
      void refetch();
    } catch (e) {
      setStatus(String(e));
    }
  };

  // copy prompts for a destination virtual path (prefilled with the source path so the
  // user just edits the mount/dir) and copies the selected file there.
  const copy = async () => {
    const name = selected();
    if (!name) return;
    const from = childLoc(name);
    const dest = (
      await promptText({
        title: "Copy file",
        label: "Destination (virtual path)",
        initial: from.path,
        confirmLabel: "Copy",
      })
    )?.trim();
    if (!dest || dest === from.path) return;
    try {
      await copyPath(from, dest);
      setStatus(`copied to ${dest}`);
      void refetch();
    } catch (e) {
      setStatus(String(e));
    }
  };

  const remove = async () => {
    const name = selected();
    if (!name) return;
    const entry = listing()?.entries.find((e) => e.name === name);
    const ok = await confirmModal({
      title: "Delete",
      message: `Delete "${name}"?`,
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await deletePath(childLoc(name), entry?.kind === "dir");
      setSelected(undefined);
      setStatus(`deleted ${name}`);
      void refetch();
    } catch (e) {
      setStatus(String(e));
    }
  };

  const download = (name: string) => {
    const a = document.createElement("a");
    a.href = fsContentURL(childLoc(name), true);
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
  };

  // Publish this pane's actions to the workspace.
  onMount(() =>
    onCleanup(
      props.register({
        kind: "browse",
        atRoot,
        go,
        selected,
        refresh: () => void refetch(),
        newFolder: () => void newFolder(),
        upload: () => fileInput.click(),
        rename: () => void rename(),
        copy: () => void copy(),
        remove: () => void remove(),
        download: () => {
          const n = selected();
          if (n) download(n);
        },
      }),
    ),
  );

  // At the virtual root each entry is a mount; distinguish local roots from workloads
  // (workloads carry a running flag) for the Kind column.
  const kindLabel = (e: FsEntry) =>
    atRoot() ? (e.running === undefined ? "local" : "workload") : e.kind;

  const onListKey = (ev: KeyboardEvent) => {
    if (ev.key === "Backspace") {
      ev.preventDefault();
      goUp();
      return;
    }
    const rows = entries();
    if (!rows.length) return;
    const i = rows.findIndex((e) => e.name === selected());
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      focusRow(rows[Math.min(rows.length - 1, i < 0 ? 0 : i + 1)].name);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      focusRow(rows[Math.max(0, i < 0 ? 0 : i - 1)].name);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      const e = rows.find((r) => r.name === selected());
      if (e) activate(e, true); // keyboard open → prompt for placement
    }
  };

  return (
    <div class="file-pane">
      {/* Breadcrumb + refresh live in the sub-header; actions in the command palette.
          The upload picker input stays here, driven hidden. */}
      <input
        ref={fileInput}
        type="file"
        multiple
        style={{ display: "none" }}
        onChange={(e) => void onUpload(e.currentTarget.files)}
      />
      <Show when={status()}>
        <p class="muted file-pane-status">{status()}</p>
      </Show>

      <Show when={listing()?.truncated}>
        <p class="badge warn">Listing truncated (directory too large)</p>
      </Show>

      <Show when={!listing.error} fallback={<p class="error">{String(listing.error)}</p>}>
        {/* Rows carry the roving tabindex, so the list itself is only a focus target
            when empty (nothing to land on) — keeping Backspace-to-go-up reachable. */}
        <div class="fs-list" tabindex={entries().length ? undefined : 0} onKeyDown={onListKey}>
          <table class="grid">
            <thead>
              <tr>
                <th>Name</th>
                <th>Size</th>
                <th>Kind</th>
                <th>Modified</th>
              </tr>
            </thead>
            <tbody>
              <Show when={!atRoot()}>
                <tr class="fs-updir" onDblClick={goUp}>
                  <td colspan="4">📁 ..</td>
                </tr>
              </Show>
              <For
                each={listing()?.entries}
                fallback={
                  <tr>
                    <td colspan="4">
                      <span class="muted">
                        {atRoot() ? "No local project and no workloads to browse." : "Empty folder."}
                      </span>
                    </td>
                  </tr>
                }
              >
                {(e) => (
                  <tr
                    classList={{
                      "fs-selected": selected() === e.name,
                      "fs-disabled": e.running === false,
                    }}
                    onClick={() => {
                      setSelected(e.name);
                      focusRow(e.name);
                    }}
                    onDblClick={() => activate(e)}
                  >
                    <td class="wrap">
                      <a
                        href="#"
                        ref={(el) => rowRefs.set(e.name, el)}
                        tabindex={rovingName() === e.name ? 0 : -1}
                        onFocus={() => setSelected(e.name)}
                        onClick={(ev) => {
                          ev.preventDefault();
                          ev.stopPropagation();
                          setSelected(e.name);
                          activate(e);
                        }}
                      >
                        <span class="fs-icon" aria-hidden="true">
                          {icon(e)}
                        </span>
                        <span class="fs-name">{e.name}</span>
                      </a>
                      <Show when={e.running === false}>
                        <span class="muted"> (stopped)</span>
                      </Show>
                      <Show when={e.linkTarget}>
                        <span class="muted"> → {e.linkTarget}</span>
                      </Show>
                    </td>
                    <td>{formatSize(e)}</td>
                    <td>{kindLabel(e)}</td>
                    <td class="muted">{e.mtime?.replace("T", " ").replace("Z", "") ?? ""}</td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </Show>
    </div>
  );
}
