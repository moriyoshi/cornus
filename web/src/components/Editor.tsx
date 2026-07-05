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

export interface EditorProps {
  content: string;
  language: "yaml" | "json" | "plain";
  onChange: (content: string) => void;
  onSave?: () => void;
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
