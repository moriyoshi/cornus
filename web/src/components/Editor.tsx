import { onCleanup, onMount, createEffect } from "solid-js";
import { EditorState } from "@codemirror/state";
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightSpecialChars,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { syntaxHighlighting, defaultHighlightStyle, bracketMatching } from "@codemirror/language";
import { yaml } from "@codemirror/lang-yaml";
import { json } from "@codemirror/lang-json";
import { claimFocus } from "../views/tiling/focusclaim";

export interface EditorProps {
  content: string;
  language: "yaml" | "json" | "plain";
  onChange: (content: string) => void;
  onSave?: () => void;
  // Whether the document should hold the keyboard — "is my pane the focused one?", exactly
  // as Term's prop of the same name. Read REACTIVELY: a pane becomes focused long after it
  // mounted (walking back to it with `prefix o`), and an editor that only claimed at mount
  // left the keys in the pane the user just left.
  autoFocus?: boolean;
}

// Editor is a CodeMirror 6 wrapper (chosen over Monaco for its touch/mobile
// support and small worker-free bundle). The parent owns the content; a
// language-appropriate mode and Ctrl/Cmd-S save hook are wired in.
export default function Editor(props: EditorProps) {
  let host!: HTMLDivElement;
  let view: EditorView | undefined;

  const language = () => {
    switch (props.language) {
      case "yaml":
        return [yaml()];
      case "json":
        return [json()];
      default:
        return [];
    }
  };

  const build = (content: string) =>
    EditorState.create({
      doc: content,
      extensions: [
        lineNumbers(),
        highlightSpecialChars(),
        highlightActiveLine(),
        history(),
        bracketMatching(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        keymap.of([
          {
            key: "Mod-s",
            run: () => {
              props.onSave?.();
              return true;
            },
          },
          indentWithTab,
          ...defaultKeymap,
          ...historyKeymap,
        ]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) props.onChange(u.state.doc.toString());
        }),
        ...language(),
      ],
    });

  onMount(() => {
    view = new EditorView({ state: build(props.content), parent: host });
  });

  // Declared after onMount so the view exists on the first pass. `hasFocus` is what keeps
  // this from fighting the user: an editor that already has the caret is left alone, so the
  // effect can re-run on every content change without stealing the cursor back from a
  // toolbar button in the same pane.
  claimFocus(
    () => !!props.autoFocus,
    () => !!view?.hasFocus,
    () => view?.focus(),
  );

  // Replace the document when the parent swaps files (content changes that did
  // not originate from this editor).
  createEffect(() => {
    const content = props.content;
    if (view && view.state.doc.toString() !== content) {
      view.setState(build(content));
    }
  });

  onCleanup(() => view?.destroy());

  return <div class="editor-wrap" ref={host} />;
}
