package inputs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/inputs"
	"github.com/moznion/uki/internal/testutil"
)

// key computes the key of task "test" (inputs: patterns) in a config where
// "other" owns b/** and docs/** is irrelevant.
func key(t *testing.T, dir, cfgHash, task string, patterns ...string) string {
	t.Helper()
	cfg := &config.Config{
		Irrelevant: []string{"docs/**"},
		Tasks: map[string]config.Task{
			task:    {Run: "x", Inputs: patterns},
			"other": {Run: "x", Inputs: []string{"b/**"}},
		},
	}
	tree, err := inputs.ReadTree(gitx.Git{Dir: dir}, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	k, ok := tree.Key(cfgHash, task, inputs.NewRules(cfg))
	if !ok {
		t.Fatal("no key for a task with inputs")
	}
	return k
}

func TestKey(t *testing.T) {
	testutil.Isolate(t)
	dir := testutil.Clone(t, testutil.Remote(t))
	testutil.WriteFile(t, dir, "a/x.go", "x")
	testutil.WriteFile(t, dir, "a/deep/y.go", "y")
	testutil.WriteFile(t, dir, "b/z.go", "z")
	testutil.WriteFile(t, dir, "docs/readme.md", "r")
	testutil.CommitAll(t, dir, "init")
	base := key(t, dir, "c", "test", "a/**")

	if k := key(t, dir, "c", "test", "a/**"); k != base {
		t.Error("key is not deterministic")
	}
	if key(t, dir, "other-config", "test", "a/**") == base {
		t.Error("config hash does not affect the key")
	}
	if key(t, dir, "c", "lint", "a/**") == base {
		t.Error("task name does not affect the key")
	}

	testutil.WriteFile(t, dir, "b/z.go", "changed")
	testutil.CommitAll(t, dir, "another task's input")
	if key(t, dir, "c", "test", "a/**") != base {
		t.Error("a change to another task's inputs changed the key")
	}
	testutil.WriteFile(t, dir, "docs/readme.md", "changed")
	testutil.CommitAll(t, dir, "irrelevant")
	if key(t, dir, "c", "test", "a/**") != base {
		t.Error("a change to an irrelevant file changed the key")
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
	modeChanged := key(t, dir, "c", "test", "a/**")
	if modeChanged == changed {
		t.Error("a mode change kept the key")
	}

	// A file nobody classified counts for every task.
	testutil.WriteFile(t, dir, "unknown.txt", "u")
	testutil.CommitAll(t, dir, "unknown")
	if key(t, dir, "c", "test", "a/**") == modeChanged {
		t.Error("adding an unclassified file kept the key")
	}

	// Uncommitted changes never count.
	before := key(t, dir, "c", "test", "a/**")
	testutil.WriteFile(t, dir, "a/x.go", "dirty")
	testutil.WriteFile(t, dir, "unknown.txt", "dirty")
	if key(t, dir, "c", "test", "a/**") != before {
		t.Error("the working tree affected the key")
	}
}

func TestKeyWithoutInputs(t *testing.T) {
	testutil.Isolate(t)
	dir := testutil.Clone(t, testutil.Remote(t))
	testutil.CommitAll(t, dir, "init")
	tree, err := inputs.ReadTree(gitx.Git{Dir: dir}, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Tasks: map[string]config.Task{"vuln": {Run: "x"}}}
	if _, ok := tree.Key("c", "vuln", inputs.NewRules(cfg)); ok {
		t.Error("a task without inputs got a key")
	}
}
