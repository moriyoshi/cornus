import { createEffect, createSignal, onCleanup, type Accessor } from "solid-js";
import type { Range } from "./catalog";

// createMetricsClock is the one beat every panel on a page refetches on.
//
// Per-panel timers would let panels drift apart, and a reader comparing two of
// them would be comparing two moments. The cadence follows the selected range —
// a 24-hour window does not change meaningfully every 15 seconds — and selecting
// a new range re-arms the interval instead of waiting out the old one.
//
// Changing the range does not advance the tick, and does not need to: the range
// is part of every panel's query key, so it refetches on its own. Ticking here
// too would just double-fetch.
//
// Must be called under an owner (inside a component), which is what disposes the
// interval with it.
export function createMetricsClock(range: Accessor<Range>): Accessor<number> {
  const [tick, setTick] = createSignal(0);
  let timer: ReturnType<typeof setInterval> | undefined;

  createEffect(() => {
    const ms = range().refreshMs;
    if (timer) clearInterval(timer);
    timer = setInterval(() => setTick((n) => n + 1), ms);
  });
  onCleanup(() => timer && clearInterval(timer));

  return tick;
}
