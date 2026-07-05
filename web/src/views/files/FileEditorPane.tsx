import { Show, createEffect, createResource, createSignal, onCleanup, onMount } from "solid-js";
import { readFsContent, writeFsContent, type FsLocation } from "../../api";
import Editor from "../../components/Editor";
import type { Pane } from "../tiling/layout";
import type { FileData, EditActions } from "./FilePane";
import { draftFor, rememberDraft } from "./drafts";
import { zoomable, zoomStyle } from "../tiling/zoom";

// FileEditorPane is an editor tile: it edits the single file named by the pane payload
// (`open` within directory `path`) in the virtual namespace. Opening a file in the
// browser (FilePane) creates one of these as a new tab or split. It fills its tile with
// the CodeMirror editor; save writes back, Ctrl/Cmd+S is wired via Editor.onSave, and
// the "files:save" command plus the sub-header Save button drive the same save().

const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

function languageFor(name: string): "yaml" | "json" | "plain" {
  if (/\.ya?ml$/i.test(name)) return "yaml";
  if (/\.json$/i.test(name)) return "json";
  return "plain";
}

export default function FileEditorPane(props: {
  pane: Pane<FileData>;
  // navigate switches this pane back to browsing the given directory (breadcrumb).
  navigate: (path: string) => void;
  register: (actions: EditActions) => () => void;
  // Whether the workspace considers this pane the focused one. Passed straight through to
  // the editor, which is where the keyboard belongs when it is — the toolbar above it is a
  // Save button and a Reload button, and neither is what you came here to press.
  focused: () => boolean;
}) {
  const filePath = () => joinPath(props.pane.data.path, props.pane.data.open ?? "");
  const loc = (): FsLocation => ({ source: "virtual", path: filePath() });

  // Unsaved text outlives this component (see ./drafts): a layout change rebuilds the
  // pane, so a draft held in component state would vanish on every move. Restoring it
  // here also means the arriving load must NOT overwrite it — hence lastSeeded starts
  // already pointing at this file when a draft came back.
  const restored = draftFor(props.pane.id, filePath());
  const [content, setContent] = createSignal(restored?.content ?? "");
  const [savedContent, setSavedContent] = createSignal(restored?.saved ?? "");
  const [status, setStatus] = createSignal("");
  const dirty = () => content() !== savedContent();
  createEffect(() =>
    rememberDraft(props.pane.id, { path: filePath(), content: content(), saved: savedContent() }),
  );

  // Load the file's text; re-loads if the pane is pointed at a different file.
  const [loaded, { refetch }] = createResource(
    () => ({ p: filePath() }),
    (src) => readFsContent({ source: "virtual", path: src.p }),
  );
  // Seed the editor whenever a fresh load lands (initial open, a re-point at another
  // file, or a reload). lastSeeded guards against re-seeding — and clobbering edits —
  // when unrelated reactivity re-runs the effect.
  let lastSeeded = restored ? filePath() : "";
  createEffect(() => {
    const text = loaded();
    if (loaded.state === "ready" && text !== undefined && filePath() !== lastSeeded) {
      lastSeeded = filePath();
      setContent(text);
      setSavedContent(text);
    }
  });

  const save = async () => {
    try {
      await writeFsContent(loc(), content());
      setSavedContent(content());
      setStatus("saved");
    } catch (e) {
      setStatus(String(e));
    }
  };
  const reload = () => {
    if (dirty() && !confirm("Discard unsaved changes?")) return;
    lastSeeded = "";
    void refetch();
  };

  onMount(() =>
    onCleanup(
      props.register({
        kind: "edit",
        go: (path) => props.navigate(path),
        refresh: reload,
        dirty,
        save: () => void save(),
      }),
    ),
  );

  return (
    // The whole pane, toolbar included, is the pinch surface, but only `--pane-zoom`'s
    // reader (the CodeMirror document) actually grows: a Save button that swelled with the
    // text would be a control moving away from the finger that is trying to press it. The
    // gesture is offered on the wider box because a pinch is aimed at "this pane", not at
    // the document's exact rectangle.
    <div
      class="file-editor"
      ref={(el) => zoomable(el, () => props.pane.id)}
      style={zoomStyle(props.pane.id)}
    >
      {/* No file name here: the tab label already carries it verbatim. */}
      <div class="row file-pane-editor-bar">
        <button class="primary" disabled={!dirty()} onClick={() => void save()}>
          Save
        </button>
        <button title="Reload from disk" onClick={reload}>
          Reload
        </button>
        <Show when={status()}>
          <span class="muted">{status()}</span>
        </Show>
        {/* The unsaved badge is last and pushed to the far right (margin-left:auto), so
            it never shifts the controls as it appears and disappears. */}
        <Show when={dirty()}>
          <span class="badge warn">unsaved</span>
        </Show>
      </div>
      <Show when={loaded.error}>
        <p class="error">{String(loaded.error)}</p>
      </Show>
      {/* The editor is built from the DRAFT, not the saved text: a rebuilt pane has to
          come back showing the unsaved edit. Editor only replaces its document when the
          two differ, so typing (doc already equals content) is not disturbed, while a
          reload or a re-point (content replaced wholesale) still swaps it. */}
      <Editor
        content={content()}
        language={languageFor(props.pane.data.open ?? "")}
        onChange={setContent}
        onSave={() => void save()}
        autoFocus={props.focused()}
      />
    </div>
  );
}
