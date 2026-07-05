import { createResource, onCleanup, type ResourceReturn } from "solid-js";

// pollResource is createResource plus a refetch interval, so status views stay
// live without a push channel.
export function pollResource<T>(fetcher: () => Promise<T>, intervalMs = 3000): ResourceReturn<T> {
  const res = createResource(fetcher);
  const timer = setInterval(() => {
    void res[1].refetch();
  }, intervalMs);
  onCleanup(() => clearInterval(timer));
  return res;
}
