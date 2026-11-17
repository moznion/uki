// Package inputs computes the key that identifies what a task's result
// depends on, so that a passing result can be reused for other commits with
// the same inputs.
package inputs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
)

// Tree lists the entries of a commit's tree, read once and shared by every
// task evaluated against that commit.
type Tree struct {
	entries []entry
}

type entry struct {
	mode, oid, path string
}

// ReadTree lists every file (and submodule) of rev with `git ls-tree`, so
// the working tree is never read: only committed content counts.
func ReadTree(g gitx.Git, rev string) (*Tree, error) {
	out, err := g.Bytes("ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	t := &Tree{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		// "<mode> SP <type> SP <oid> TAB <path>"
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("ls-tree %s: unexpected entry %q", rev, rec)
		}
		f := bytes.Fields(meta)
		if len(f) != 3 {
			return nil, fmt.Errorf("ls-tree %s: unexpected entry %q", rev, rec)
		}
		t.entries = append(t.entries, entry{mode: string(f[0]), oid: string(f[2]), path: string(path)})
	}
	sort.Slice(t.entries, func(i, j int) bool { return t.entries[i].path < t.entries[j].path })
	return t, nil
}

// Key identifies the inputs of task: the hash of uki.yaml at the commit
// (configSHA256, so any change to the config invalidates every key), the
// task name, and the mode, object id and path of every file matching
// patterns. Two commits get the same key exactly when all of these agree.
func (t *Tree) Key(configSHA256, task string, patterns []string) string {
	match := config.InputMatcher(patterns)
	h := sha256.New()
	fmt.Fprintf(h, "uki-inputs v1\nconfig %s\ntask %s\n", configSHA256, task)
	for _, e := range t.entries {
		if match(e.path) {
			fmt.Fprintf(h, "%s %s %s\x00", e.mode, e.oid, e.path)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
