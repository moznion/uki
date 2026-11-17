package gitx_test

import (
	"errors"
	"testing"

	"github.com/moznion/uki/internal/gitx"
	"github.com/moznion/uki/internal/testutil"
)

func TestIsDirty(t *testing.T) {
	testutil.Isolate(t)
	dir := testutil.Clone(t, testutil.Remote(t))
	testutil.WriteFile(t, dir, "a.txt", "a")
	testutil.CommitAll(t, dir, "init")
	g := gitx.Git{Dir: dir}

	assertDirty := func(want bool, why string) {
		t.Helper()
		got, err := g.IsDirty()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: IsDirty = %v, want %v", why, got, want)
		}
	}
	assertDirty(false, "clean")
	testutil.WriteFile(t, dir, "sub/untracked.txt", "x")
	assertDirty(true, "untracked file in subdirectory")
	testutil.Git(t, dir, "clean", "-fdq")
	testutil.WriteFile(t, dir, "a.txt", "changed")
	assertDirty(true, "modified")
	testutil.Git(t, dir, "add", "a.txt")
	assertDirty(true, "staged")
}

func TestBranchAndShow(t *testing.T) {
	testutil.Isolate(t)
	remote := testutil.Remote(t)
	dir := testutil.Clone(t, remote)
	testutil.WriteFile(t, dir, "a.txt", "hello")
	sha := testutil.CommitAll(t, dir, "init")
	g := gitx.Git{Dir: dir}

	if b, err := g.CurrentBranch(); err != nil || b != "main" {
		t.Errorf("CurrentBranch = %q, %v", b, err)
	}
	testutil.Git(t, dir, "checkout", "--quiet", "--detach")
	if b, err := g.CurrentBranch(); err != nil || b != "" {
		t.Errorf("detached CurrentBranch = %q, %v", b, err)
	}

	if b, err := g.Show(sha, "a.txt"); err != nil || string(b) != "hello" {
		t.Errorf("Show = %q, %v", b, err)
	}
	if _, err := g.Show(sha, "missing.txt"); !errors.Is(err, gitx.ErrNotFound) {
		t.Errorf("Show missing: %v", err)
	}
	if got, err := g.ResolveCommit("HEAD"); err != nil || got != sha {
		t.Errorf("ResolveCommit = %q, %v", got, err)
	}
	if !g.HasCommit(sha) || g.HasCommit("0123456789012345678901234567890123456789") {
		t.Error("HasCommit wrong")
	}
}

func TestRemoteBranch(t *testing.T) {
	testutil.Isolate(t)
	remote := testutil.Remote(t)
	dir := testutil.Clone(t, remote)
	sha := testutil.CommitAll(t, dir, "init")
	g := gitx.Git{Dir: dir}

	if ok, err := g.RemoteBranchExists("origin", "feature"); err != nil || ok {
		t.Fatalf("before push: %v, %v", ok, err)
	}
	testutil.Git(t, dir, "push", "--quiet", "origin", "HEAD:refs/heads/feature")
	if ok, err := g.RemoteBranchExists("origin", "feature"); err != nil || !ok {
		t.Fatalf("after push: %v, %v", ok, err)
	}
	ref, err := g.FetchBranch("origin", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, dir, "rev-parse", ref); got != sha {
		t.Errorf("%s = %s, want %s", ref, got, sha)
	}
}
