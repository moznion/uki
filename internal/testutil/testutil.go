// Package testutil builds throwaway git repositories for tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Isolate points git at a fresh global config so the developer's own settings
// (signing, hooks, default branch, ...) cannot leak into tests.
func Isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			_ = os.Unsetenv(k)
			t.Cleanup(func() { _ = os.Setenv(k, v) })
		}
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	content := `[user]
	name = uki-test
	email = test@example.com
[init]
	defaultBranch = main
[commit]
	gpgsign = false
[advice]
	detachedHead = false
`
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := GitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// GitErr runs git in dir and returns trimmed combined output.
func GitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Remote creates a bare repository acting as "origin".
func Remote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	Git(t, ".", "init", "--quiet", "--bare", dir)
	return dir
}

// Clone clones remote into a new temp dir.
func Clone(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	Git(t, ".", "clone", "--quiet", remote, dir)
	return dir
}

// WriteFile writes a file relative to dir, creating parents.
func WriteFile(t *testing.T, dir, path, content string) {
	t.Helper()
	p := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// CommitAll stages everything and commits, returning the new HEAD.
func CommitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "--quiet", "--allow-empty", "-m", msg)
	return Git(t, dir, "rev-parse", "HEAD")
}

// SSHKey generates an ed25519 key pair and returns the private key path and
// the public key line.
func SSHKey(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found")
	}
	priv := filepath.Join(t.TempDir(), "id_ed25519")
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", priv).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return priv, strings.TrimSpace(string(pub))
}

// UseSSHSigning configures dir's repository to sign with the given key.
func UseSSHSigning(t *testing.T, dir, priv string) {
	t.Helper()
	Git(t, dir, "config", "gpg.format", "ssh")
	Git(t, dir, "config", "user.signingkey", priv)
}
