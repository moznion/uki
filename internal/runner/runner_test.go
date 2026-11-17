package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/uki/internal/config"
)

func TestRunSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := Run(context.Background(), t.TempDir(), "hello", config.Task{Run: "echo out; echo err >&2"}, &stdout, &stderr)

	if !res.Success || res.ExitCode != 0 {
		t.Fatalf("want success, got %+v", res)
	}
	if res.Name != "hello" || res.Command != "echo out; echo err >&2" {
		t.Errorf("name/command not recorded: %+v", res)
	}
	if stdout.String() != "out\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "err\n") || !strings.Contains(stderr.String(), "==> [hello] ok") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if res.StartedAt.IsZero() {
		t.Error("StartedAt not set")
	}
}

func TestRunLogHash(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := Run(context.Background(), t.TempDir(), "h", config.Task{Run: "printf 'a\\nb\\n'"}, &stdout, &stderr)
	// The log hash covers exactly the task's own output.
	sum := sha256.Sum256([]byte("a\nb\n"))
	if res.LogSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("log hash = %s, stdout = %q", res.LogSHA256, stdout.String())
	}
}

// Writing to stdout and stderr concurrently must not race on the log hash.
func TestRunConcurrentStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	script := "for i in $(seq 1 2000); do echo out$i; echo err$i >&2; done"
	res := Run(context.Background(), t.TempDir(), "c", config.Task{Run: script}, &stdout, &stderr)
	if !res.Success || len(res.LogSHA256) != 64 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunFailureExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := Run(context.Background(), t.TempDir(), "bad", config.Task{Run: "exit 3"}, &stdout, &stderr)
	if res.Success || res.ExitCode != 3 {
		t.Fatalf("want exit 3, got %+v", res)
	}
	if !strings.Contains(stderr.String(), "FAILED (exit 3)") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	res := Run(context.Background(), dir, "env",
		config.Task{Run: `printf '%s|%s' "$UKI_A" "$(pwd -P)"`, Env: map[string]string{"UKI_A": "x y"}},
		&stdout, &stderr)
	if !res.Success {
		t.Fatalf("failed: %s", stderr.String())
	}
	got := strings.SplitN(stdout.String(), "|", 2)
	if got[0] != "x y" {
		t.Errorf("env not injected: %q", stdout.String())
	}
	if want, _ := filepath.EvalSymlinks(dir); got[1] != want {
		t.Errorf("not run in dir %s: %q", dir, got[1])
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	res := Run(ctx, t.TempDir(), "x", config.Task{Run: "sleep 5"}, &stdout, &stderr)
	if res.Success {
		t.Fatal("cancelled task must not succeed")
	}
}
