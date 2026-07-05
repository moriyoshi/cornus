package webbff

// Batch transfers and preflight.
//
// The SPA drags N rows at a time and used to issue N requests, each of which could
// fail on its own with no shared account of what happened. A batch does the same work in
// one request and reports PER ITEM: an item that fails does not abort the ones after it,
// because a partial transfer the user cannot see the shape of is worse than either
// outcome on its own.
//
// Preflight answers the question the UI could not previously ask: what will this
// actually do? It resolves both ends of every item, runs the same planner the real
// operation will run, and reports the route, the permissions, whether something will be
// overwritten, how much data is involved, and every warning it can see in advance —
// without touching anything. That matters most for the limits a user cannot otherwise
// anticipate: the per-file size cap that still applies on a relayed copy, symlinks that
// will be stepped over, and directory listings too large to enumerate.

import (
	"context"
	"fmt"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// ---- wire shapes ----

// fsTransferItem is one source, and optionally an explicit destination. With To empty
// the item lands under the request's To directory as its own basename, which is what
// every drag gesture means.
type fsTransferItem struct {
	From string `json:"from"`
	To   string `json:"to,omitempty"`
}

// fsTransferRequest is the body of /fs/copy, /fs/move and /fs/preflight.
//
// Items empty keeps the ORIGINAL single-item contract: the source is the request query's
// path and To is the exact destination, answered with the legacy response shape. That is
// deliberate — the mock, the SPA and the existing tests all speak it, and a batch is an
// extension rather than a replacement.
type fsTransferRequest struct {
	To    string           `json:"to"`
	Items []fsTransferItem `json:"items,omitempty"`
}

// fsItemResult is what became of one item of a batch.
type fsItemResult struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"` // "ok" | "failed"
	Error  string `json:"error,omitempty"`
	// Skipped names symlinks the transfer deliberately stepped over. For a MOVE a
	// non-empty Skipped means the source was kept, so the item did not complete.
	Skipped []string `json:"skipped,omitempty"`
}

// fsBatchResponse is a batch's outcome. Result is "ok" when every item succeeded,
// "partial" when some did, "failed" when none did.
type fsBatchResponse struct {
	Result string         `json:"result"`
	Items  []fsItemResult `json:"items"`
}

// fsPreflightItem is what WOULD happen to one item.
type fsPreflightItem struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"` // dir | file | symlink
	// Action is the effect on the destination: "create" (nothing there), "overwrite"
	// (a file replaced), "merge" (a directory copied into an existing one), or
	// "refused".
	Action string `json:"action"`
	// Route is where the work will run: "here" (the developer's filesystem, no daemon
	// round trip), "server", or "relay" (streamed through this process).
	Route  string `json:"route"`
	Native bool   `json:"native,omitempty"`
	Why    string `json:"why,omitempty"`
	// Files and Bytes size the transfer. Truncated marks an estimate that stopped at a
	// bound rather than finishing.
	Files     int      `json:"files,omitempty"`
	Bytes     int64    `json:"bytes,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	// Error is set when the operation would be refused; Action is then "refused".
	Error string `json:"error,omitempty"`
}

// fsPreflightResponse is the whole report, with totals so a UI can summarise without
// re-adding the items.
type fsPreflightResponse struct {
	Op       string            `json:"op"`
	Items    []fsPreflightItem `json:"items"`
	Refusals int               `json:"refusals"`
	Warnings int               `json:"warnings"`
	Files    int               `json:"files"`
	Bytes    int64             `json:"bytes"`
}

// ---- shared resolution ----

// transferPairs expands a request into concrete (src, dst) virtual paths. It is shared
// by the batch and the preflight so the report cannot describe different work from the
// one that runs.
func transferPairs(base fsQuery, req fsTransferRequest) ([]fsTransferItem, error) {
	if len(req.Items) == 0 {
		if req.To == "" {
			return nil, statusErr(400, "missing destination")
		}
		return []fsTransferItem{{From: base.path, To: req.To}}, nil
	}
	out := make([]fsTransferItem, 0, len(req.Items))
	for _, it := range req.Items {
		if it.From == "" {
			return nil, statusErr(400, "item is missing a source path")
		}
		to := it.To
		if to == "" {
			if req.To == "" {
				return nil, statusErr(400, "missing destination directory for %q", it.From)
			}
			to = joinChild(req.To, pathpkg.Base(strings.TrimSuffix(it.From, "/")))
		}
		out = append(out, fsTransferItem{From: it.From, To: to})
	}
	return out, nil
}

// maxPreflightNodes bounds the directory walk a preflight does. A report is worth having
// quickly; an exact count of a million-file tree is not worth the wait, and the estimate
// says when it stopped short.
const maxPreflightNodes = 5000

// ---- batch ----

// FsBatch runs a copy or a move over every item, continuing past failures.
//
// Each item is independent: one refusal must not silently strand the rest, and the
// caller needs to know exactly which ones landed. Ordering is preserved so a UI can line
// the results up against what it sent.
func (s *Server) FsBatch(ctx context.Context, op fsOp, base fsQuery, items []fsTransferItem) fsBatchResponse {
	out := fsBatchResponse{Items: make([]fsItemResult, 0, len(items))}
	okCount := 0
	for _, it := range items {
		src := fsQuery{source: base.source, workload: base.workload, root: base.root, path: it.From}
		dst := fsQuery{source: base.source, workload: base.workload, root: base.root, path: it.To}
		res := fsItemResult{From: it.From, To: it.To, Status: "ok"}

		var skipped []string
		var err error
		if op == opMove {
			skipped, err = s.FsMove(ctx, src, dst)
		} else {
			skipped, err = s.FsCopy(ctx, src, dst)
		}
		res.Skipped = skipped
		switch {
		case err != nil:
			res.Status, res.Error = "failed", err.Error()
		case op == opMove && len(skipped) > 0:
			// The source was deliberately kept, so the move did not complete. Calling
			// that "ok" would report a file as moved while it sits where it started.
			res.Status = "failed"
			res.Error = fmt.Sprintf("kept the source: %d entr%s skipped",
				len(skipped), plural(len(skipped), "y", "ies"))
		default:
			okCount++
		}
		out.Items = append(out.Items, res)
	}
	switch {
	case okCount == len(items):
		out.Result = "ok"
	case okCount == 0:
		out.Result = "failed"
	default:
		out.Result = "partial"
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---- preflight ----

// FsPreflight reports what a copy or move WOULD do, without doing any of it.
func (s *Server) FsPreflight(ctx context.Context, op fsOp, base fsQuery, items []fsTransferItem) fsPreflightResponse {
	out := fsPreflightResponse{Op: op.String(), Items: make([]fsPreflightItem, 0, len(items))}
	for _, it := range items {
		p := s.preflightOne(ctx, op, base, it)
		if p.Error != "" {
			out.Refusals++
		} else {
			out.Files += p.Files
			out.Bytes += p.Bytes
		}
		out.Warnings += len(p.Warnings)
		out.Items = append(out.Items, p)
	}
	return out
}

func (s *Server) preflightOne(ctx context.Context, op fsOp, base fsQuery, it fsTransferItem) fsPreflightItem {
	p := fsPreflightItem{From: it.From, To: it.To, Action: "refused"}
	refuse := func(format string, a ...any) fsPreflightItem {
		p.Error = fmt.Sprintf(format, a...)
		return p
	}

	src := fsQuery{source: base.source, workload: base.workload, root: base.root, path: it.From}
	dst := fsQuery{source: base.source, workload: base.workload, root: base.root, path: it.To}

	srcStat, err := s.FsStat(ctx, src)
	if err != nil {
		return refuse("%s", err.Error())
	}
	p.Kind = srcStat.Kind

	// The real operation lands an item INSIDE an existing destination directory. Mirror
	// that here or the report names a path the transfer will not use.
	if d, err := s.FsStat(ctx, dst); err == nil && d.Kind == "dir" && srcStat.Kind != "dir" {
		dst.path = joinChild(dst.path, pathpkg.Base(strings.TrimSuffix(it.From, "/")))
		p.To = dst.path
	}

	srcSite, err := s.site(ctx, src)
	if err != nil {
		return refuse("%s", err.Error())
	}
	dstSite, err := s.site(ctx, dst)
	if err != nil {
		return refuse("%s", err.Error())
	}

	// Permissions. A move mutates both ends, so a read-only SOURCE refuses it too.
	if op == opMove {
		if err := siteWritable(srcSite); err != nil {
			return refuse("%s", err.Error())
		}
	}
	if err := siteWritable(dstSite); err != nil {
		return refuse("%s", err.Error())
	}

	plan := planTransfer(op, srcSite, dstSite, s.serverFSOps(ctx))
	p.Route, p.Native, p.Why = plan.where.String(), plan.native, plan.why

	// The same bytes on both ends. This is what a drop into the folder something already
	// lives in resolves to, and it is worth naming as THAT rather than as a generic
	// refusal — the two are indistinguishable from the resolved paths, but only one of
	// them is a gesture a user actually made on purpose.
	if sitePath(srcSite) == sitePath(dstSite) {
		if pathpkg.Dir(strings.TrimSuffix(it.From, "/")) == pathpkg.Dir(strings.TrimSuffix(p.To, "/")) {
			return refuse("%s is already in this folder", pathpkg.Base(strings.TrimSuffix(it.From, "/")))
		}
		return refuse("the source and the destination are the same file; nothing to %s", op)
	}

	// Refusals the operation itself would raise, reported before the user commits.
	if srcStat.Kind == "dir" {
		if src.path == "" || pathpkg.Base(src.path) == "." {
			return refuse("cannot %s a mount root", op)
		}
		if withinPath(sitePath(dstSite), sitePath(srcSite)) {
			return refuse("cannot %s a folder into itself", op)
		}
	}

	// What the destination will become.
	switch d, err := s.FsStat(ctx, dst); {
	case err != nil:
		p.Action = "create"
	case d.Kind == "dir" && srcStat.Kind == "dir":
		p.Action = "merge"
		p.Warnings = append(p.Warnings, "an existing folder will be merged into, not replaced")
	case d.Kind != srcStat.Kind:
		p.Action = "overwrite"
		p.Warnings = append(p.Warnings,
			fmt.Sprintf("a %s will be replaced by a %s", d.Kind, srcStat.Kind))
	default:
		p.Action = "overwrite"
	}

	// Size, and the limits that bite at this size.
	if srcStat.Kind == "dir" {
		p.Files, p.Bytes, p.Truncated, p.Warnings = s.estimateTree(ctx, src, p.Warnings)
		if w := relayCostWarning(plan, p.Bytes, it.From); w != "" {
			p.Warnings = append(p.Warnings, w)
		}
		if p.Truncated {
			p.Warnings = append(p.Warnings,
				fmt.Sprintf("stopped counting at %d entries; the transfer itself is not bounded by this", maxPreflightNodes))
		}
	} else {
		p.Files, p.Bytes = 1, srcStat.Size
		if w := relayCostWarning(plan, srcStat.Size, it.From); w != "" {
			p.Warnings = append(p.Warnings, w)
		}
	}
	if srcStat.Kind == "symlink" {
		p.Warnings = append(p.Warnings, "a symlink is copied as the file it points at, or stepped over if it does not resolve")
	}
	return p
}

// relayCostWarning flags a transfer big enough that the user should know it is going the
// long way round. It is NOT a cap any more — streaming removed that — but a relayed copy
// still pulls every byte through this process and back out, where a client-side one never
// leaves the disk. Saying so is the difference between a slow transfer and a mysterious
// one.
//
// Silent below the threshold, and silent for any route that is not a relay: a warning
// attached to transfers that are actually cheap is noise, and noise is what makes the
// real warnings unreadable.
const relayCostWarnBytes = 64 << 20

func relayCostWarning(plan fsPlan, size int64, name string) string {
	if plan.where != execRelay || size < relayCostWarnBytes {
		return ""
	}
	return fmt.Sprintf("%s is %s and will be streamed through this process, not copied in place",
		pathpkg.Base(name), humanBytes(size))
}

// humanBytes formats a size for a person rather than a machine.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %sB", float64(n)/float64(div), []string{"K", "M", "G", "T"}[exp])
}

// sitePath is a site's ABSOLUTE, comparable identity.
//
// A siteClient path is relative to its own root, so two paths from different roots can
// read identically ("tree" under the project and "tree" under a bind source) while naming
// unrelated directories. Comparing those directly reports a folder as being copied into
// itself when it is not — and, in the other direction, misses a genuine overlap when one
// directory is reachable through two roots. Joining the root back on removes both.
func sitePath(s fsSite) string {
	if s.kind == siteClient {
		return filepath.Join(s.root.Real, filepath.FromSlash(s.path))
	}
	// Container and server paths are already absolute, but only within their workload.
	return s.workload + ":" + s.path
}

// withinPath reports whether p is q or lives beneath it, on path boundaries.
func withinPath(p, q string) bool {
	return p == q || strings.HasPrefix(p, strings.TrimSuffix(q, "/")+"/")
}

// estimateTree walks a directory to size it, bounded by maxPreflightNodes. It uses the
// same FsList every level of the real copy uses, so a directory the transfer cannot
// enumerate is one the estimate cannot either — and says so.
func (s *Server) estimateTree(ctx context.Context, dir fsQuery, warnings []string) (files int, bytes int64, truncated bool, out []string) {
	out = warnings
	var walk func(q fsQuery, depth int)
	walk = func(q fsQuery, depth int) {
		if truncated || files >= maxPreflightNodes {
			truncated = files >= maxPreflightNodes
			return
		}
		if depth > maxCopyDepth {
			out = append(out, fmt.Sprintf("%s is deeper than %d levels and will be refused", q.path, maxCopyDepth))
			truncated = true
			return
		}
		listing, err := s.FsList(ctx, q)
		if err != nil {
			out = append(out, fmt.Sprintf("%s could not be listed: %s", q.path, err.Error()))
			truncated = true
			return
		}
		if listing.Truncated {
			out = append(out, fmt.Sprintf("%s has more entries than one listing can carry and will be refused", q.path))
			truncated = true
			return
		}
		for _, e := range listing.Entries {
			child := q
			child.path = joinChild(q.path, e.Name)
			switch e.Kind {
			case "dir":
				walk(child, depth+1)
			case "symlink":
				out = append(out, fmt.Sprintf("%s is a symlink and may be stepped over", child.path))
			default:
				files++
				bytes += e.Size
			}
			if truncated {
				return
			}
		}
	}
	walk(dir, 0)
	return files, bytes, truncated, out
}
