// Splitting a virtual path into the mount it names and the path inside that mount.
//
// This is the browser's half of the rule the BFF applies in resolveVirtual
// (cmd/cornus/internal/webbff/fs.go): the FIRST segment of a virtual path is a mount —
// a local root id ("project", "mount0", …) or a workload name — and the remainder is
// that mount's own sub-path, which for a workload is an absolute path inside the
// container. `/.cornus/web/fs?source=virtual&path=web/src/app` and a terminal opened on
// workload `web` in `/src/app` have to mean the same place, and this is what makes them
// agree.
//
// The rule cannot be shared across the language boundary, so it is stated on both sides
// and pinned by a test on each. What is NOT duplicated here is resolveVirtual's decision
// about what the mount IS: the BFF checks its own local-root table and treats anything
// else as a workload, which is right for it (an unknown mount should surface as a normal
// container not-found). A caller here wants the opposite — to know before it acts whether
// there is a running workload to attach to — so it asks the workload list instead, which
// can also tell "not a container" apart from "not running".

export interface MountPath {
  // The first path segment: a local root id or a workload name. Empty at the virtual
  // root, where the entries are the mounts themselves and nothing is inside anything.
  mount: string;
  // The path within that mount, always absolute and always cleaned, so a mount's own
  // root is "/" rather than "". That is what a container path looks like, and it is what
  // an exec's working directory has to be.
  dir: string;
}

export function mountPathOf(vpath: string): MountPath {
  const rel = vpath.replace(/^\/+/, "").replace(/\/+$/, "");
  if (rel === "") return { mount: "", dir: "/" };
  const i = rel.indexOf("/");
  if (i === -1) return { mount: rel, dir: "/" };
  return { mount: rel.slice(0, i), dir: "/" + rel.slice(i + 1) };
}

// virtualPathOf is the inverse: the virtual path naming `dir` inside `mount`. It
// exists for the direction a terminal pushes — a session reports a container path
// ("/srv/app") and a file pane has to be told where that is in the workspace
// ("shop-web/srv/app").
//
// Round-tripping through mountPathOf is the contract, so a mount's own root has to
// come back as the bare mount rather than "shop-web/": the trailing slash names
// the same place but is a DIFFERENT string, and pane paths are compared as strings
// to decide whether a follow still has anywhere to go.
export function virtualPathOf(mount: string, dir: string): string {
  if (!mount) return "";
  const rel = dir.replace(/^\/+/, "").replace(/\/+$/, "");
  return rel === "" ? mount : `${mount}/${rel}`;
}
