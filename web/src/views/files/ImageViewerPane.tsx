import { Show, createSignal } from "solid-js";
import { fsContentURL } from "../../api";
import type { Pane } from "../tiling/layout";
import type { FileData } from "./FilePane";

// ImageViewerPane is a tiny image preview tile: it shows the file named by the pane
// payload (`open` within directory `path`) centered and fit to the tile. Opening an
// image in the browser (FilePane) creates one of these as a new tab or split. The BFF
// serves inline image reads with the real image content-type, so a plain <img> renders
// them (raster and SVG).
const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

export default function ImageViewerPane(props: { pane: Pane<FileData> }) {
  const name = () => props.pane.data.open ?? "";
  const src = () => fsContentURL({ source: "virtual", path: joinPath(props.pane.data.path, name()) });
  const [failed, setFailed] = createSignal(false);
  return (
    <div class="image-viewer">
      <Show
        when={!failed()}
        fallback={<p class="muted">Could not load {name()}.</p>}
      >
        <img class="image-viewer-img" src={src()} alt={name()} onError={() => setFailed(true)} />
      </Show>
    </div>
  );
}
