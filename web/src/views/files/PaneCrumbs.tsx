import { For, createEffect, createSignal, onCleanup, onMount } from "solid-js";
import type { Pane } from "../tiling/layout";
import type { FileData } from "./FilePane";

// PaneCrumbs renders a Files pane's virtual-path breadcrumb in its title bar. "All" is
// the virtual root (the mount list); each segment navigates the pane there via onGo
// (which routes through the pane's own go(), so the unsaved-changes guard and the
// selection/editor reset still apply). Links are non-draggable so grabbing a crumb
// never starts a pane drag; clicks are stopped from bubbling into the header's drag /
// focus handlers.
//
// Overflow behaviour: the strip is left-aligned while it fits. When the path is longer
// than the title, we scroll it to the end so the current (deepest) folder stays in
// view, and a left fade marks the clipped ancestors — a left-side ellipsis that plain
// text-overflow can't give across separate crumb links. scrollLeft is set on an
// overflow:hidden element (scrollable programmatically, no visible scrollbar), so when
// everything fits it clamps to 0 and the fade stays off.
export default function PaneCrumbs(props: { pane: Pane<FileData>; onGo: (path: string) => void }) {
  let nav!: HTMLElement;
  const [clipped, setClipped] = createSignal(false);

  const crumbs = () => {
    const parts = props.pane.data.path ? props.pane.data.path.split("/") : [];
    const out: { label: string; path: string }[] = [];
    let acc = "";
    for (const p of parts) {
      acc = acc ? `${acc}/${p}` : p;
      out.push({ label: p, path: acc });
    }
    return out;
  };

  // anchorEnd keeps the deepest crumb visible and reports whether anything is clipped
  // on the left (so the fade only shows when it should).
  const anchorEnd = () => {
    if (!nav) return;
    nav.scrollLeft = nav.scrollWidth;
    setClipped(nav.scrollLeft > 0);
  };
  // Re-anchor on navigation…
  createEffect(() => {
    props.pane.data.path;
    anchorEnd();
  });
  // …and when the pane (hence the title width) is resized.
  onMount(() => {
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => anchorEnd());
    ro.observe(nav);
    onCleanup(() => ro.disconnect());
  });

  const go = (e: MouseEvent, path: string) => {
    e.preventDefault();
    e.stopPropagation();
    props.onGo(path);
  };
  return (
    <nav ref={nav} class="crumbs pane-crumbs" classList={{ "is-clipped": clipped() }}>
      <a href="#" draggable={false} onClick={(e) => go(e, "")}>
        All
      </a>
      <For each={crumbs()}>
        {(c) => (
          <>
            <span class="crumb-sep">/</span>
            <a href="#" draggable={false} onClick={(e) => go(e, c.path)}>
              {c.label}
            </a>
          </>
        )}
      </For>
    </nav>
  );
}
