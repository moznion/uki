// Package gitx is a thin wrapper around the git CLI.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Git struct {
	Dir string
}

// Bytes runs git and returns raw stdout.
func (g Git) Bytes(args ...string) ([]byte, error) {
	// git calls are short-lived and not cancelled individually.
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // fixed binary; args are built by uki, not passed to a shell
	cmd.Dir = g.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Run runs git and returns trimmed stdout.
func (g Git) Run(args ...string) (string, error) {
	b, err := g.Bytes(args...)
	return strings.TrimSpace(string(b)), err
}

func (g Git) TopLevel() (string, error) { return g.Run("rev-parse", "--show-toplevel") }

func (g Git) ResolveCommit(rev string) (string, error) {
	return g.Run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
}

func (g Git) TreeOf(rev string) (string, error) { return g.Run("rev-parse", rev+"^{tree}") }

func (g Git) HasCommit(sha string) bool {
	_, err := g.Run("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// CurrentBranch returns "" on detached HEAD.
func (g Git) CurrentBranch() (string, error) {
	b, err := g.Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if b == "HEAD" {
		return "", nil
	}
	return b, nil
}

// IsDirty reports whether there are staged, unstaged or untracked changes.
func (g Git) IsDirty() (bool, error) {
	out, err := g.Run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// ErrNotFound is returned by Show when the path does not exist at the revision.
var ErrNotFound = errors.New("not found")

// Show returns the content of path at rev.
func (g Git) Show(rev, path string) ([]byte, error) {
	if _, err := g.Run("cat-file", "-e", rev+":"+path); err != nil {
		return nil, ErrNotFound
	}
	return g.Bytes("show", rev+":"+path)
}

// RemoteBranchExists asks the remote directly, so it does not depend on local refs.
func (g Git) RemoteBranchExists(remote, branch string) (bool, error) {
	out, err := g.Run("ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// FetchBranch updates refs/remotes/<remote>/<branch> and returns that ref name.
func (g Git) FetchBranch(remote, branch string) (string, error) {
	ref := "refs/remotes/" + remote + "/" + branch
	_, err := g.Run("fetch", "--quiet", "--no-tags", remote, "+refs/heads/"+branch+":"+ref)
	return ref, err
}
