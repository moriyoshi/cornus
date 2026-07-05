package blockcache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// entry is one cached file (its .data backing file) considered for eviction.
type entry struct {
	key     string
	dataArg string // full path to the .data file
	idxArg  string // full path to the .idx sidecar
	size    int64
	mod     time.Time
}

// Prune reclaims disk in an on-disk block cache rooted at root. It evicts whole
// files (the .data + .idx pair) in two passes:
//
//  1. TTL: any file whose backing .data has not been modified within olderThan is
//     removed (olderThan <= 0 skips this pass).
//  2. Size cap: if the remaining cache still exceeds maxBytes, files are removed
//     oldest-first (by .data mtime, an LRU approximation since reads touch it)
//     until the total is at or below maxBytes (maxBytes <= 0 skips this pass).
//
// A missing root is not an error. freed is the number of files removed; err
// reports the first read/remove failure but does not stop the sweep.
func Prune(root string, olderThan time.Duration, maxBytes int64) (freed int, err error) {
	entries, ferr := collect(root)
	if ferr != nil {
		if errors.Is(ferr, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, ferr
	}

	var firstErr error
	remove := func(e entry) {
		derr := os.Remove(e.dataArg)
		ierr := os.Remove(e.idxArg)
		if derr != nil && !errors.Is(derr, fs.ErrNotExist) && firstErr == nil {
			firstErr = derr
		}
		if ierr != nil && !errors.Is(ierr, fs.ErrNotExist) && firstErr == nil {
			firstErr = ierr
		}
		freed++
	}

	// Pass 1: TTL eviction.
	kept := entries[:0]
	if olderThan > 0 {
		cutoff := time.Now().Add(-olderThan)
		for _, e := range entries {
			if e.mod.Before(cutoff) {
				remove(e)
			} else {
				kept = append(kept, e)
			}
		}
	} else {
		kept = entries
	}

	// Pass 2: size-cap eviction, oldest first.
	if maxBytes > 0 {
		var total int64
		for _, e := range kept {
			total += e.size
		}
		if total > maxBytes {
			sort.Slice(kept, func(i, j int) bool { return kept[i].mod.Before(kept[j].mod) })
			for _, e := range kept {
				if total <= maxBytes {
					break
				}
				remove(e)
				total -= e.size
			}
		}
	}

	return freed, firstErr
}

// DiskUsage reports the total size and count of the cache's backing files under
// root — the same apparent-size measure Prune's size cap uses, so the two agree.
// A missing root reports zero, not an error.
func DiskUsage(root string) (bytes int64, files int, err error) {
	entries, err := collect(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, e := range entries {
		bytes += e.size
		files++
	}
	return bytes, files, nil
}

// collect enumerates the .data files under root's shard directories.
func collect(root string) ([]entry, error) {
	shards, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		dir := filepath.Join(root, shard.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return out, err
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".data") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return out, err
			}
			key := strings.TrimSuffix(name, ".data")
			out = append(out, entry{
				key:     key,
				dataArg: filepath.Join(dir, name),
				idxArg:  filepath.Join(dir, key+".idx"),
				size:    info.Size(),
				mod:     info.ModTime(),
			})
		}
	}
	return out, nil
}
