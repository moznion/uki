package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitFor makes a task that announces itself and then waits (up to 5s) for
// another task's announcement: it passes only when both run at once.
func waitFor(self, other string) string {
	return `mkdir -p out && touch out/` + self + ` && i=0; while [ ! -f out/` + other + ` ]; do i=$((i+1)); [ $i -gt 100 ] && exit 1; sleep 0.05; done`
}

const parallelConfig = `
tasks:
  a: { run: "` + "%A%" + `" }
  b: { run: "` + "%B%" + `" }
branches: [ { match: "**", tasks: [a, b] } ]
`

func parallelEnv(t *testing.T, extra string) *env {
	t.Helper()
	cfg := strings.NewReplacer("%A%", waitFor("a", "b"), "%B%", waitFor("b", "a")).Replace(parallelConfig) + extra
	return newEnv(t, cfg, ".gitignore", "/out/\n")
}

func TestRunParallel(t *testing.T) {
	e := parallelEnv(t, "")
	expect(t, e.uki(e.dev, "run", "-j", "2"), 0, "==> [a] started", "==> [b] started", "uki: recorded")
	expect(t, e.uki(e.dev, "status"), 0, "success: Passed: a, b")
}

func TestRunSequentialByDefault(t *testing.T) {
	e := parallelEnv(t, "")
	// One at a time, a waits for b in vain.
	expect(t, e.uki(e.dev, "run", "--no-record"), 1, "[a] FAILED")
}

func TestRunJobsFromConfig(t *testing.T) {
	e := parallelEnv(t, "jobs: 2\n")
	expect(t, e.uki(e.dev, "run", "--no-record"), 0)
	// -j overrides the config (clear the first run's marks first).
	if err := os.RemoveAll(filepath.Join(e.dev, "out")); err != nil {
		t.Fatal(err)
	}
	expect(t, e.uki(e.dev, "run", "--no-record", "-j", "1"), 1)
}

func TestRunLocksSerializeTasks(t *testing.T) {
	// Both take the same directory as a mutex; overlapping runs fail.
	hold := `mkdir -p out && mkdir out/held || exit 1; sleep 0.3; rmdir out/held`
	cfg := `
tasks:
  a: { run: "` + hold + `", locks: [dir] }
  b: { run: "` + hold + `", locks: [dir] }
  c: { run: "` + hold + `" }
branches: [ { match: "**", tasks: [a, b] } ]
`
	e := newEnv(t, cfg, ".gitignore", "/out/\n")
	expect(t, e.uki(e.dev, "run", "-j", "2"), 0, "uki: recorded")
	// The same tasks without a shared lock collide.
	expect(t, e.uki(e.dev, "run", "--no-record", "-j", "2", "a", "c"), 1)
}

func TestRunParallelKeepsEachOutputTogether(t *testing.T) {
	cfg := `
tasks:
  a: { run: "echo A1; sleep 0.3; echo A2" }
  b: { run: "sleep 0.1; echo B1; sleep 0.3; echo B2" }
branches: [ { match: "**", tasks: [a, b] } ]
`
	e := newEnv(t, cfg)
	r := e.uki(e.dev, "run", "-j", "2")
	expect(t, r, 0, "A1\nA2\n", "B1\nB2\n")
}

func TestRunParallelFailFastStopsOthers(t *testing.T) {
	cfg := `
tasks:
  bad:  { run: "sleep 0.2; exit 3" }
  slow: { run: "sleep 8; echo SLOW-DONE" }
  later: { run: "echo LATER" }
branches: [ { match: "**", tasks: [bad, slow, later] } ]
`
	e := newEnv(t, cfg)
	began := time.Now()
	r := e.uki(e.dev, "run", "-j", "2", "--fail-fast")
	expect(t, r, 1, "[bad] FAILED (exit 3)", "[slow] stopped after another task failed", "uki: recorded")
	if took := time.Since(began); took > 6*time.Second {
		t.Errorf("fail-fast waited for the slow task (%s)", took)
	}
	if strings.Contains(r.stdout, "SLOW-DONE") || strings.Contains(r.stdout, "LATER") {
		t.Errorf("tasks kept running or started after the failure:\n%s", r.stdout)
	}
	rec := readRecordAt(t, e, e.head(e.dev))
	if _, ok := rec.Tasks["slow"]; ok {
		t.Error("the stopped task was recorded")
	}
	if rec.Tasks["bad"].Success {
		t.Error("the failure was not recorded")
	}
}

func TestRunParallelStoresArtifacts(t *testing.T) {
	e := newArtifactEnv(t, artifactConfig)
	expect(t, e.uki(e.dev, "run", "-j", "2", "cover", "broken"), 1, "uki: recorded")
	if got := e.uki(e.dev, "artifact", "get", "cover", "out/cover.out"); got.stdout != "COV 1" {
		t.Errorf("artifact = %q", got.stdout)
	}
}

func TestRunJobsFlagErrors(t *testing.T) {
	e := parallelEnv(t, "")
	expect(t, e.uki(e.dev, "run", "-j", "-1"), 2, "must not be negative")
}
