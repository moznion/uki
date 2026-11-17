package inputs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/inputs"
	"github.com/moznion/uki/internal/testutil"
)

func key(t *testing.T, dir, cfg, task string, patterns ...string) string {
	t.Helper()
	tree, err := inputs.ReadTree(gitx.Git{Dir: dir}, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return tree.Key(cfg, task, patterns)
}

func TestKey(t *testing.T) {
	testutil.Isolate(t)
	dir := testutil.Clone(t, testutil.Remote(t))
	testutil.WriteFile(t, dir, "a/x.go", "x")
	testutil.WriteFile(t, dir, "a/deep/y.go", "y")
	testutil.WriteFile(t, dir, "b/z.go", "z")
	testutil.CommitAll(t, dir, "init")
	base := key(t, dir, "c", "test", "a/**")

	if k := key(t, dir, "c", "test", "a/**"); k != base {
		t.Error("key is not deterministic")
	}
	if key(t, dir, "other", "test", "a/**") == base {
		t.Error("config hash does not affect the key")
	}
	if key(t, dir, "c", "lint", "a/**") == base {
		t.Error("task name does not affect the key")
	}
	if key(t, dir, "c", "test", "a/*") == base {
		t.Error("`*` must not cross `/`, so a/deep/y.go must drop out")
	}

	testutil.WriteFile(t, dir, "b/z.go", "changed")
	testutil.CommitAll(t, dir, "unrelated")
	if key(t, dir, "c", "test", "a/**") != base {
		t.Error("a change outside the patterns changed the key")
	}

	testutil.WriteFile(t, dir, "a/deep/y.go", "changed")
	testutil.CommitAll(t, dir, "related")
	changed := key(t, dir, "c", "test", "a/**")
	if changed == base {
		t.Error("a change inside the patterns kept the key")
	}

	if err := os.Chmod(filepath.Join(dir, "a/x.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.CommitAll(t, dir, "mode")
	if key(t, dir, "c", "test", "a/**") == changed {
		t.Error("a mode change kept the key")
	}

	testutil.WriteFile(t, dir, "a/new.go", "n")
	testutil.CommitAll(t, dir, "added")
	if key(t, dir, "c", "test", "a/x.go", "a/deep/y.go") == key(t, dir, "c", "test", "a/**") {
		t.Error("an added matching file kept the key")
	}

	// Uncommitted changes never count.
	before := key(t, dir, "c", "test", "a/**")
	testutil.WriteFile(t, dir, "a/x.go", "dirty")
	if key(t, dir, "c", "test", "a/**") != before {
		t.Error("the working tree affected the key")
	}
}
