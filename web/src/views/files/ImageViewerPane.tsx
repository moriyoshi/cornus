import { Show, createSignal } from "solid-js";
import { fsContentURL } from "../../api";
import type { Pane } from "../tiling/layout";
import type { FileData } from "./FilePane";
import { claimFocus, holdsFocus } from "../tiling/focusclaim";
import { zoomable, zoomStyle } from "../tiling/zoom";

// ImageViewerPane is a tiny image preview tile: it shows the file named by the pane
// payload (`open` within directory `path`) centered and fit to the tile. Opening an
// image in the browser (FilePane) creates one of these as a new tab or split. The BFF
// serves inline image reads with the real image content-type, so a plain <img> renders
// them (raster and SVG).
const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

export default function ImageViewerPane(props: { pane: Pane<FileData>; focused: () => boolean }) {
  const name = () => props.pane.data.open ?? "";
  const src = () => fsContentURL({ source: "virtual", path: joinPath(props.pane.data.path, name()) });
  const [failed, setFailed] = createSignal(false);
  // A viewer has nothing to type into, and that is exactly why it needs the claim: without
  // one, walking onto it leaves the keyboard in the pane you came FROM — a terminal, where
  // the next keystroke is a command. `tabindex="-1"` makes the tile itself the place the
  // keys land; it is not in the Tab order, because there is nothing here to operate.
  let root!: HTMLDivElement;
  claimFocus(
    () => props.focused(),
    () => holdsFocus(root),
    () => root.focus(),
  );
  return (
    <div
      class="image-viewer"
      tabindex={-1}
      // The scroll container is also the pinch surface, which is the right element for both:
      // zooming past the tile is what makes the scrolling necessary, and the two would fight
      // if a finger could pan on one box and pinch on another.
      ref={(el) => {
        root = el;
        zoomable(el, () => props.pane.id);
      }}
      style={zoomStyle(props.pane.id)}
    >
      <Show
        when={!failed()}
        fallback={<p class="muted">Could not load {name()}.</p>}
      >
        <img class="image-viewer-img" src={src()} alt={name()} onError={() => setFailed(true)} />
      </Show>
    </div>
  );
}
