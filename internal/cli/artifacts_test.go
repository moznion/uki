package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/testutil"
)

const artifactConfig = `
tasks:
  cover:
    run: "mkdir -p out && printf 'COV %s' \"$COV\" > out/cover.out"
    env: { COV: "1" }
    artifacts: [out/cover.out]
  broken:
    run: "printf partial > out/broken.out; exit 2"
    artifacts: [out/broken.out, out/never.out]
branches:
  - match: "**"
    tasks: [cover]
`

func newArtifactEnv(t *testing.T, cfg string) *env {
	t.Helper()
	return newEnv(t, cfg, ".gitignore", "/out/\n")
}

func TestRunStoresArtifacts(t *testing.T) {
	e := newArtifactEnv(t, artifactConfig)
	expect(t, e.uki(e.dev, "run"), 0, "uki: recorded")
	sha := e.head(e.dev)

	expect(t, e.uki(e.dev, "status"), 0, "artifact out/cover.out (5 B)")
	r := e.uki(e.dev, "artifact", "get", "cover", "out/cover.out")
	if r.code != 0 || r.stdout != "COV 1" {
		t.Fatalf("artifact get = %q (exit %d)\n%s", r.stdout, r.code, r.stderr)
	}

	// The CI checkout is shallow and need not contain the commit itself.
	ci := e.ci()
	dst := filepath.Join(t.TempDir(), "sub", "cover.out")
	expect(t, e.uki(ci, "artifact", "get", "--rev", sha, "-o", dst, "cover", "out/cover.out"), 0)
	if b, err := os.ReadFile(dst); err != nil || string(b) != "COV 1" {
		t.Fatalf("-o wrote %q, %v", b, err)
	}

	stored := testutil.Git(t, e.dev, "show", "origin/uki/records:"+record.ArtifactPath(sha, "cover", "out/cover.out"))
	if stored != "COV 1" {
		t.Errorf("records branch holds %q", stored)
	}
	rec := readRecordAt(t, e, sha)
	if a := rec.Tasks["cover"].Artifacts["out/cover.out"]; a.Size != 5 || a.SHA256 != sha256Hex([]byte("COV 1")) {
		t.Errorf("record metadata: %+v", a)
	}
}

func TestRunStoresArtifactsOfFailedTaskAndWarnsAboutMissingOnes(t *testing.T) {
	e := newArtifactEnv(t, artifactConfig)
	r := e.uki(e.dev, "run", "cover", "broken")
	expect(t, r, 1, "[broken] artifact out/never.out not stored: file does not exist", "uki: recorded")

	got := e.uki(e.dev, "artifact", "get", "broken", "out/broken.out")
	if got.code != 0 || got.stdout != "partial" {
		t.Errorf("failed task's artifact = %q (exit %d) %s", got.stdout, got.code, got.stderr)
	}
	expect(t, e.uki(e.dev, "artifact", "get", "broken", "out/never.out"), 1, "stored no artifact out/never.out")
	expect(t, e.uki(e.dev, "artifact", "get", "nope", "out/x"), 1, `has no task "nope"`)
}

func TestRunReplacesArtifactsOnRerun(t *testing.T) {
	e := newArtifactEnv(t, `
tasks:
  count:
    run: "mkdir -p out && n=$(cat out/n 2>/dev/null || echo 0) && n=$((n+1)) && echo $n > out/n && printf run$n > out/run.out"
    artifacts: [out/run.out]
branches: [ { match: "**", tasks: [count] } ]
`)
	expect(t, e.uki(e.dev, "run"), 0)
	expect(t, e.uki(e.dev, "run"), 0)
	if got := e.uki(e.dev, "artifact", "get", "count", "out/run.out"); got.stdout != "run2" {
		t.Errorf("artifact after re-run = %q, want run2", got.stdout)
	}
}

func TestRunSkipsOversizedAndNonRegularArtifacts(t *testing.T) {
	e := newArtifactEnv(t, `
tasks:
  gen:
    run: "mkdir -p out && printf 1234 > out/big && printf 123 > out/fit && ln -sf ../uki.yaml out/link"
    artifacts: [out/big, out/fit, out/link]
branches: [ { match: "**", tasks: [gen] } ]
record: { max_artifact_bytes: 3 }
`)
	expect(t, e.uki(e.dev, "run"), 0,
		"artifact out/big not stored: larger than record.max_artifact_bytes (3)",
		"artifact out/link not stored: not a regular file",
		"uki: recorded")
	rec := readRecordAt(t, e, e.head(e.dev))
	if a := rec.Tasks["gen"].Artifacts; len(a) != 1 || a["out/fit"].Size != 3 {
		t.Errorf("stored artifacts: %+v", a)
	}
}

func TestRunRejectsTrackedArtifacts(t *testing.T) {
	e := newEnv(t, `
tasks: { gen: { run: "echo GEN", artifacts: [tracked.txt] } }
branches: [ { match: "**", tasks: [gen] } ]
`, "tracked.txt", "x")
	r := e.uki(e.dev, "run")
	expect(t, r, 1, `artifact "tracked.txt" is tracked by git`)
	if strings.Contains(r.stdout, "GEN") {
		t.Error("tasks ran although the config is invalid")
	}
}

func TestRunNoRecordIgnoresArtifacts(t *testing.T) {
	e := newArtifactEnv(t, artifactConfig)
	r := e.uki(e.dev, "run", "--no-record", "broken")
	expect(t, r, 1)
	if strings.Contains(r.stderr, "not stored") {
		t.Errorf("artifacts were collected without recording:\n%s", r.stderr)
	}
}

func TestArtifactGetRejectsTamperedContent(t *testing.T) {
	e := newArtifactEnv(t, artifactConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	sha := e.head(e.dev)
	pushToRecords(t, e, func(wt string) {
		testutil.WriteFile(t, wt, record.ArtifactPath(sha, "cover", "out/cover.out"), "COV 9")
	})
	expect(t, e.uki(e.dev, "artifact", "get", "cover", "out/cover.out"), 1, "does not match the digest")

	pushToRecords(t, e, func(wt string) {
		if err := os.RemoveAll(filepath.Join(wt, "artifacts")); err != nil {
			t.Fatal(err)
		}
	})
	expect(t, e.uki(e.dev, "artifact", "get", "cover", "out/cover.out"), 1, "missing from the records branch")
}

func TestArtifactGetErrors(t *testing.T) {
	e := newArtifactEnv(t, artifactConfig)
	expect(t, e.uki(e.dev, "artifact"), 2, "Usage: uki artifact get")
	expect(t, e.uki(e.dev, "artifact", "get", "cover"), 2, "Usage: uki artifact get")
	expect(t, e.uki(e.dev, "artifact", "get", "cover", "../etc/passwd"), 1, "can exist")
	expect(t, e.uki(e.dev, "artifact", "get", "cover", "out/cover.out"), 1, "records branch uki/records not found")
	expect(t, e.uki(e.dev, "run", "--no-record"), 0)
	testutil.CommitAll(t, e.dev, "other")
	expect(t, e.uki(e.dev, "run"), 0)
	expect(t, e.uki(e.dev, "artifact", "get", "--rev", "HEAD~1", "cover", "out/cover.out"), 1, "no uki record for")
}

func TestMarkOutputJSON(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	sha := e.head(e.dev)
	r := e.uki(e.ci(), "mark", "--dry-run", "--output", "json", "--records-after", e.recordsTip())
	expect(t, r, 0)
	var got []markedCommit
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.stdout)
	}
	want := markedCommit{SHA: sha, Branch: "main", State: "success", Description: "Passed: lint, test"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want [%+v]", got, want)
	}

	// Nothing judged still prints a valid (empty) array.
	r = e.uki(e.ci(), "mark", "--dry-run", "--output", "json", "--records-before", e.recordsTip(), "--records-after", e.recordsTip())
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Errorf("empty run printed %q", r.stdout)
	}
	expect(t, e.uki(e.ci(), "mark", "--dry-run", "--output", "yaml", "--sha", sha), 1, "--output must be text or json")
}

func readRecordAt(t *testing.T, e *env, sha string) *record.Record {
	t.Helper()
	testutil.Git(t, e.dev, "fetch", "--quiet", "origin", "+refs/heads/uki/records:refs/remotes/origin/uki/records")
	rec, err := record.Parse([]byte(testutil.Git(t, e.dev, "show", "origin/uki/records:"+record.Path(sha))))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}
