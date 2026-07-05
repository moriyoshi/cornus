import { Show, createEffect, createResource, createSignal, onCleanup, onMount } from "solid-js";
import { readFsContent, writeFsContent, type FsLocation } from "../../api";
import Editor from "../../components/Editor";
import type { Pane } from "../tiling/layout";
import type { FileData, EditActions } from "./FilePane";

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
}) {
  const filePath = () => joinPath(props.pane.data.path, props.pane.data.open ?? "");
  const loc = (): FsLocation => ({ source: "virtual", path: filePath() });

  const [content, setContent] = createSignal("");
  const [savedContent, setSavedContent] = createSignal("");
  const [status, setStatus] = createSignal("");
  const dirty = () => content() !== savedContent();

  // Load the file's text; re-loads if the pane is pointed at a different file.
  const [loaded, { refetch }] = createResource(
    () => ({ p: filePath() }),
    (src) => readFsContent({ source: "virtual", path: src.p }),
  );
  // Seed the editor whenever a fresh load lands (initial open, a re-point at another
  // file, or a reload). lastSeeded guards against re-seeding — and clobbering edits —
  // when unrelated reactivity re-runs the effect.
  let lastSeeded = "";
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
    <div class="file-editor">
      <div class="row file-pane-editor-bar">
        <strong>{props.pane.data.open}</strong>
        <Show when={dirty()}>
          <span class="badge warn">unsaved</span>
        </Show>
        <button class="primary" disabled={!dirty()} onClick={() => void save()}>
          Save
        </button>
        <button title="Reload from disk" onClick={reload}>
          Reload
        </button>
        <Show when={status()}>
          <span class="muted">{status()}</span>
        </Show>
      </div>
      <Show when={loaded.error}>
        <p class="error">{String(loaded.error)}</p>
      </Show>
      <Editor
        content={savedContent()}
        language={languageFor(props.pane.data.open ?? "")}
        onChange={setContent}
        onSave={() => void save()}
      />
    </div>
  );
}
