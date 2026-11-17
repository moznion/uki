package recorder_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/recorder"
	"github.com/moznion/uki/internal/testutil"
)

const workflow = ".github/workflows/uki.yml"

// setup returns a clone with one pushed commit containing a workflow file.
func setup(t *testing.T) (remote, dir, sha, tree string) {
	t.Helper()
	testutil.Isolate(t)
	remote = testutil.Remote(t)
	dir = testutil.Clone(t, remote)
	testutil.WriteFile(t, dir, workflow, "name: uki\n")
	testutil.WriteFile(t, dir, "main.go", "package main\n")
	sha = testutil.CommitAll(t, dir, "init")
	testutil.Git(t, dir, "push", "--quiet", "origin", "main")
	tree = testutil.Git(t, dir, "rev-parse", "HEAD^{tree}")
	return remote, dir, sha, tree
}

func newRecorder(dir string) recorder.Recorder {
	return recorder.Recorder{
		Git:       gitx.Git{Dir: dir},
		Remote:    "origin",
		Branch:    "uki/records",
		SeedFiles: []string{workflow},
	}
}

func readRecord(t *testing.T, dir, sha string) *record.Record {
	t.Helper()
	testutil.Git(t, dir, "fetch", "--quiet", "origin", "+refs/heads/uki/records:refs/remotes/origin/uki/records")
	out := testutil.Git(t, dir, "show", "origin/uki/records:"+record.Path(sha))
	r, err := record.Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSaveCreatesOrphanBranch(t *testing.T) {
	_, dir, sha, tree := setup(t)
	results := map[string]record.TaskResult{"lint": {Command: "true", Success: true, ConfigSHA256: "c"}}
	if err := newRecorder(dir).Save(sha, tree, results, nil); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "fetch", "--quiet", "origin", "uki/records")

	// Only the record and the seeded workflow; no project files, no parents.
	files := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", "FETCH_HEAD")
	if want := workflow + "\n" + record.Path(sha); files != want {
		t.Errorf("records branch files:\n%s\nwant:\n%s", files, want)
	}
	if n := testutil.Git(t, dir, "rev-list", "--count", "FETCH_HEAD"); n != "1" {
		t.Errorf("records branch has %s commits, want 1 (orphan)", n)
	}

	r := readRecord(t, dir, sha)
	if r.SHA != sha || r.Tree != tree || !r.Tasks["lint"].Success {
		t.Errorf("record = %+v", r)
	}

	msg := testutil.Git(t, dir, "log", "-1", "--format=%B", "FETCH_HEAD")
	for _, want := range []string{"Uki-Target: " + sha, "Uki-Tree: " + tree, "Uki-Task: lint success=true"} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}

	// Temporary worktree and branch are cleaned up; the user's checkout is untouched.
	if wt := testutil.Git(t, dir, "worktree", "list", "--porcelain"); strings.Count(wt, "worktree ") != 1 {
		t.Errorf("leftover worktrees:\n%s", wt)
	}
	if b := testutil.Git(t, dir, "branch", "--list", "uki-records-tmp-*"); b != "" {
		t.Errorf("leftover temp branch: %s", b)
	}
	if st := testutil.Git(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("working tree changed: %s", st)
	}
	if b := testutil.Git(t, dir, "branch", "--show-current"); b != "main" {
		t.Errorf("current branch = %s", b)
	}
}

func TestSaveMergesIntoExistingRecord(t *testing.T) {
	_, dir, sha, tree := setup(t)
	rec := newRecorder(dir)
	if err := rec.Save(sha, tree, map[string]record.TaskResult{"lint": {Success: false, ExitCode: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(sha, tree, map[string]record.TaskResult{"test": {Success: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := rec.Save(sha, tree, map[string]record.TaskResult{"lint": {Success: true}}, nil); err != nil {
		t.Fatal(err)
	}
	r := readRecord(t, dir, sha)
	if len(r.Tasks) != 2 || !r.Tasks["lint"].Success || !r.Tasks["test"].Success {
		t.Errorf("tasks = %+v", r.Tasks)
	}
	if n := testutil.Git(t, dir, "rev-list", "--count", "origin/uki/records"); n != "3" {
		t.Errorf("records branch has %s commits, want 3", n)
	}
}

func TestSaveConcurrentClonesAllLand(t *testing.T) {
	remote, dir, _, _ := setup(t)

	const n = 4
	type target struct{ dir, sha, tree string }
	targets := make([]target, n)
	for i := range n {
		d := testutil.Clone(t, remote)
		testutil.WriteFile(t, d, fmt.Sprintf("f%d", i), "x")
		sha := testutil.CommitAll(t, d, fmt.Sprintf("c%d", i))
		targets[i] = target{d, sha, testutil.Git(t, d, "rev-parse", "HEAD^{tree}")}
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	logs := make([]bytes.Buffer, n)
	for i, tg := range targets {
		wg.Go(func() {
			r := newRecorder(tg.dir)
			r.MaxTries = 20
			r.Log = &logs[i]
			errs[i] = r.Save(tg.sha, tg.tree, map[string]record.TaskResult{"lint": {Success: true}}, nil)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("clone %d: %v\n%s", i, err, logs[i].String())
		}
	}
	for _, tg := range targets {
		if r := readRecord(t, dir, tg.sha); !r.Tasks["lint"].Success {
			t.Errorf("record for %s missing", tg.sha)
		}
	}
	// The workflow is seeded only once, by whoever created the branch.
	if n := testutil.Git(t, dir, "log", "--format=%H", "origin/uki/records", "--", workflow); strings.Count(n, "\n") != 0 {
		t.Errorf("workflow seeded more than once:\n%s", n)
	}
}

func TestSaveSigns(t *testing.T) {
	_, dir, sha, tree := setup(t)
	priv, _ := testutil.SSHKey(t)
	testutil.UseSSHSigning(t, dir, priv)

	r := newRecorder(dir)
	r.Sign = true
	if err := r.Save(sha, tree, map[string]record.TaskResult{"lint": {Success: true}}, nil); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "fetch", "--quiet", "origin", "uki/records")
	raw := testutil.Git(t, dir, "cat-file", "commit", "FETCH_HEAD")
	if !strings.Contains(raw, "gpgsig -----BEGIN SSH SIGNATURE-----") {
		t.Errorf("record commit is not SSH-signed:\n%s", raw)
	}
}

// A failing pre-push hook in the user's repository must not block recording.
func TestSaveSkipsPrePushHook(t *testing.T) {
	_, dir, sha, tree := setup(t)
	testutil.WriteFile(t, dir, ".git/hooks/pre-push", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, ".git/hooks/pre-push"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := newRecorder(dir).Save(sha, tree, map[string]record.TaskResult{"lint": {Success: true}}, nil); err != nil {
		t.Fatalf("Save blocked by pre-push hook: %v", err)
	}
}

func TestSaveRejectsInvalidSHA(t *testing.T) {
	_, dir, _, tree := setup(t)
	for _, bad := range []string{"", "HEAD", "abc", "../../etc/passwd"} {
		if err := newRecorder(dir).Save(bad, tree, nil, nil); err == nil {
			t.Errorf("Save(%q) succeeded", bad)
		}
	}
}

func TestSaveFailsWithoutRemote(t *testing.T) {
	_, dir, sha, tree := setup(t)
	r := newRecorder(dir)
	r.Remote = "nope"
	if err := r.Save(sha, tree, map[string]record.TaskResult{"lint": {Success: true}}, nil); err == nil {
		t.Error("expected error for unknown remote")
	}
}

func TestSaveReplacesArtifactsOfRerunTasksOnly(t *testing.T) {
	_, dir, sha, tree := setup(t)
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := map[string]record.Artifact{"out/a.txt": {SHA256: "x", Size: 4}}
	rec := newRecorder(dir)
	results := map[string]record.TaskResult{
		"lint": {Success: true, Artifacts: meta},
		"test": {Success: true, Artifacts: meta},
	}
	files := recorder.Artifacts{"lint": {"out/a.txt": src}, "test": {"out/a.txt": src}}
	if err := rec.Save(sha, tree, results, files); err != nil {
		t.Fatal(err)
	}
	// lint runs again and produces nothing: its old artifact goes, test's stays.
	if err := rec.Save(sha, tree, map[string]record.TaskResult{"lint": {Success: true}}, nil); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "fetch", "--quiet", "origin", "+refs/heads/uki/records:refs/remotes/origin/uki/records")
	got := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", "origin/uki/records", "artifacts/")
	if want := record.ArtifactPath(sha, "test", "out/a.txt"); got != want {
		t.Errorf("artifacts = %q, want %q", got, want)
	}
	if r := readRecord(t, dir, sha); len(r.Tasks["lint"].Artifacts) != 0 || len(r.Tasks["test"].Artifacts) != 1 {
		t.Errorf("record artifacts: %+v", r.Tasks)
	}
}

func TestSaveRejectsUnlistedArtifacts(t *testing.T) {
	_, dir, sha, tree := setup(t)
	files := recorder.Artifacts{"lint": {"out/a.txt": "/nonexistent"}}
	err := newRecorder(dir).Save(sha, tree, map[string]record.TaskResult{"lint": {Success: true}}, files)
	if err == nil || !strings.Contains(err.Error(), "not listed in the result") {
		t.Errorf("err = %v", err)
	}
}
