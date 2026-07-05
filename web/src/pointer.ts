// What kind of pointer is driving the app, asked of the DEVICE rather than of a gesture.
//
// This is the other half of a question src/dnd.ts already answers, and the two are
// deliberately different: a drag knows which pointer started it (`ev.pointerType`), so it
// picks its transport from the finger or mouse actually in play. A standing SETTING has no
// event to read — "as a tab on touch devices" has to be answered at the moment a command
// runs, whatever ran it (a tap, a prefix key, the palette). A media query is the only thing
// that can answer it.
//
// `(pointer: coarse)` asks about the PRIMARY pointer, and that is the point of choosing it
// over `any-pointer`. A laptop with a touchscreen matches `(any-pointer: coarse)` and still
// wants panes side by side: it has the screen for them and a mouse to arrange them with.
// The device this is for is the one where a finger is the only pointer there is.
//
// Read per call, never cached: a tablet gains and loses a pointing device when its keyboard
// case is attached, and answering from a value sampled at page load would leave the app
// behaving as the wrong kind of device until a reload. There is nothing to cache anyway —
// matchMedia is a lookup, and this runs once per pane created.
//
// The `?? false` is not only for jsdom: matchMedia is absent in any non-DOM host, and the
// safe answer to "I cannot tell" is the disposition that predates touch support.
export function coarsePointer(): boolean {
  return globalThis.matchMedia?.("(pointer: coarse)").matches ?? false;
}
