// Unsaved editor text, held OUTSIDE the pane component that displays it.
//
// A pane is not a long-lived component: every layout operation rebuilds the tree, and a
// pane that lands under a different node is unmounted and mounted afresh (drag a tab to
// another tile's edge, stack it elsewhere, close a neighbour and collapse the split).
// Anything the editor kept in component state died with that rebuild — and did so
// silently, because the new instance re-reads the file and looks like a clean editor of
// the same name. The pane ID survives every one of those operations, so the draft is
// keyed by it and outlives the component.
//
// Scope is the page: this is a plain module-level Map, not persisted. A reload starts
// with no drafts, which matches the layout's own persistence (it restores WHICH files
// are open, never their unsaved contents).

export interface Draft {
  // The file the draft belongs to. A pane re-pointed at another file must not inherit
  // the previous file's text, so a mismatch here reads as "no draft".
  path: string;
  content: string;
  // The text last read from (or written to) the server — what `content` is compared
  // against to decide dirtiness. It has to travel with the draft: restoring the text
  // without it would make a saved file look dirty and a dirty one look saved.
  saved: string;
}

const drafts = new Map<string, Draft>();

// draftFor returns the pane's draft only if it belongs to the file the pane now shows.
export function draftFor(paneId: string, path: string): Draft | undefined {
  const d = drafts.get(paneId);
  return d && d.path === path ? d : undefined;
}

export function rememberDraft(paneId: string, draft: Draft): void {
  drafts.set(paneId, draft);
}

// forgetDraft is called when a pane stops editing for good — closed, or navigated back
// to browsing. A move must NOT call it: that is the case this module exists for.
export function forgetDraft(paneId: string): void {
  drafts.delete(paneId);
}
