package cli

import (
	"strings"
	"testing"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/testutil"
)

const reuseConfig = `
inputs: [shared.txt]
tasks:
  a: { run: "echo RUN-A; grep -q good a/x", inputs: ["a/**"] }
  b: { run: "echo RUN-B", inputs: ["b/**"] }
  always: { run: "echo RUN-ALWAYS" }
branches:
  - match: "**"
    tasks: [a, b, always]
`

func newReuseEnv(t *testing.T) *env {
	t.Helper()
	return newEnv(t, reuseConfig, "a/x", "good", "b/x", "1", "shared.txt", "1")
}

// change commits content at path on the current branch and pushes it.
func (e *env) change(path, content string) string {
	e.t.Helper()
	testutil.WriteFile(e.t, e.dev, path, content)
	sha := testutil.CommitAll(e.t, e.dev, "change "+path)
	testutil.Git(e.t, e.dev, "push", "--quiet", "origin", "HEAD")
	return sha
}

// ran reports whether task printed its marker line ("RUN-A" must not match "RUN-ALWAYS").
func ran(r result, task string) bool { return strings.Contains(r.stdout, "RUN-"+task+"\n") }

func TestRunReusesResultsWithUnchangedInputs(t *testing.T) {
	e := newReuseEnv(t)
	first := e.head(e.dev)
	expect(t, e.uki(e.dev, "run"), 0, "RUN-A", "RUN-B", "RUN-ALWAYS")

	sha := e.change("b/x", "2")
	r := e.uki(e.dev, "run")
	expect(t, r, 0, "[a] reused the passing result of "+first[:12], "RUN-B", "RUN-ALWAYS", "uki: recorded")
	if ran(r, "A") {
		t.Errorf("a ran although its inputs did not change\nstdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
	}
	expect(t, e.uki(e.dev, "status"), 0, "ok (reused "+first[:12]+")", "success: Passed: a, b, always")
	expect(t, e.uki(e.ci(), "mark", "--dry-run", "--sha", sha, "--branch", "main"), 0, "success Passed: a, b, always")

	rec := readRecordAt(t, e, sha)
	if a := rec.Tasks["a"]; a.ReusedFrom != first || a.InputsSHA256 == "" || a.DurationMS != 0 || a.Branch != "main" {
		t.Errorf("reused result: %+v", a)
	}
	if rec.Tasks["always"].InputsSHA256 != "" {
		t.Error("a task without inputs got an inputs key")
	}
}

func TestRunRerunsWhenInputsChange(t *testing.T) {
	e := newReuseEnv(t)
	expect(t, e.uki(e.dev, "run"), 0)

	e.change("a/x", "good again")
	if r := e.uki(e.dev, "run"); !ran(r, "A") || ran(r, "B") {
		t.Errorf("own input changed: a ran=%v, b ran=%v", ran(r, "A"), ran(r, "B"))
	}
	e.change("shared.txt", "2")
	if r := e.uki(e.dev, "run"); !ran(r, "A") || !ran(r, "B") {
		t.Error("a change to the top-level inputs did not re-run every task with inputs")
	}
	e.change(config.FileName, reuseConfig+"# edited\n")
	if r := e.uki(e.dev, "run"); !ran(r, "A") || !ran(r, "B") {
		t.Error("a change to uki.yaml did not re-run every task")
	}
}

func TestRunReuseIsOptional(t *testing.T) {
	e := newReuseEnv(t)
	expect(t, e.uki(e.dev, "run"), 0)
	e.change("b/x", "2")

	expect(t, e.uki(e.dev, "run", "--no-record", "a"), 0, "RUN-A")
	expect(t, e.uki(e.dev, "run", "--no-reuse", "a"), 0, "RUN-A", "uki: recorded")
	// Re-running the same commit runs again instead of reusing itself.
	expect(t, e.uki(e.dev, "run", "a"), 0, "RUN-A")
	// --no-reuse still indexed its run, so the next commit can reuse it.
	next := e.change("b/x", "3")
	r := e.uki(e.dev, "run", "a")
	expect(t, r, 0, "[a] reused")
	if readRecordAt(t, e, next).Tasks["a"].ReusedFrom == "" {
		t.Error("not recorded as reused")
	}
}

func TestRunDoesNotReuseFailures(t *testing.T) {
	e := newEnv(t, reuseConfig, "a/x", "bad", "b/x", "1", "shared.txt", "1")
	first := e.head(e.dev)
	expect(t, e.uki(e.dev, "run"), 1, "RUN-A")
	// Failures are never indexed; forge an entry to check that run refuses it anyway.
	key := readRecordAt(t, e, first).Tasks["a"].InputsSHA256
	pushToRecords(t, e, func(wt string) {
		testutil.WriteFile(t, wt, record.IndexPath(key), `{"sha":"`+first+`","task":"a"}`)
	})
	e.change("b/x", "2")
	r := e.uki(e.dev, "run")
	expect(t, r, 1, "RUN-A")
	if strings.Contains(r.stderr, "reused") {
		t.Error("a failed result was reused")
	}
}

func TestRunReusesAcrossNonAncestors(t *testing.T) {
	e := newReuseEnv(t)
	expect(t, e.uki(e.dev, "run"), 0)
	testutil.Git(t, e.dev, "checkout", "--quiet", "-b", "feature/x")
	e.change("b/x", "2")
	expect(t, e.uki(e.dev, "run"), 0)

	// Like a rebase: a new commit beside the recorded one, same inputs for a.
	testutil.Git(t, e.dev, "checkout", "--quiet", "-b", "feature/y", "main")
	sha := e.change("b/x", "3")
	expect(t, e.uki(e.dev, "run", "a"), 0, "[a] reused the passing result of")
	// Either recorded commit is a valid source, neither is an ancestor of sha.
	if from := readRecordAt(t, e, sha).Tasks["a"].ReusedFrom; from == "" || from == sha {
		t.Fatalf("reused_from = %q", from)
	}
	expect(t, e.uki(e.dev, "status", "--branch", "feature/y"), statusExitPending, "ok (reused")
}

func TestMarkRejectsReuseThatDefaultBranchRulesForbid(t *testing.T) {
	e := newReuseEnv(t)
	expect(t, e.uki(e.dev, "run"), 0)
	testutil.Git(t, e.dev, "checkout", "--quiet", "-b", "feature/x")
	// The branch narrows a's inputs and gives "always" some, then runs once.
	loose := strings.Replace(reuseConfig, `inputs: ["a/**"]`, `inputs: ["nothing/**"]`, 1)
	loose = strings.Replace(loose, `always: { run: "echo RUN-ALWAYS" }`, `always: { run: "echo RUN-ALWAYS", inputs: ["nothing/**"] }`, 1)
	e.change(config.FileName, loose)
	expect(t, e.uki(e.dev, "run"), 0, "RUN-A", "RUN-ALWAYS")
	// Breaking a/x is invisible to the branch's own rules...
	sha := e.change("a/x", "bad")
	r := e.uki(e.dev, "run")
	expect(t, r, 0, "[a] reused", "[always] reused")

	// ...but GitHub judges with the default branch's uki.yaml.
	ci := e.ci()
	r = e.uki(ci, "mark", "--dry-run", "--sha", sha, "--branch", "feature/x")
	expect(t, r, 0, "error", "Reused result not valid: a, always",
		"[a] reused result is not valid: inputs differ from",
		"[always] reused result is not valid: the task declares no inputs")
}

func TestArtifactGetFollowsReusedResult(t *testing.T) {
	e := newEnv(t, `
tasks:
  cover:
    run: "mkdir -p out && git rev-parse HEAD > out/c"
    inputs: ["src/**"]
    artifacts: [out/c]
branches: [ { match: "**", tasks: [cover] } ]
`, ".gitignore", "/out/\n", "src/a", "1")
	first := e.head(e.dev)
	expect(t, e.uki(e.dev, "run"), 0)
	e.change("README", "x")
	expect(t, e.uki(e.dev, "run"), 0, "[cover] reused")
	r := e.uki(e.dev, "artifact", "get", "cover", "out/c")
	if r.code != 0 || strings.TrimSpace(r.stdout) != first {
		t.Errorf("artifact of reused result = %q (exit %d) %s", r.stdout, r.code, r.stderr)
	}
}
