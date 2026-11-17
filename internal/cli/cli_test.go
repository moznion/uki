package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moznion/uki/internal/config"
	"github.com/moznion/uki/internal/github"
	"github.com/moznion/uki/internal/record"
	"github.com/moznion/uki/internal/testutil"
)

const baseConfig = `
tasks:
  lint: { run: "echo LINT" }
  test: { run: "echo TEST" }
  bad:  { run: "echo BAD; exit 3" }
branches:
  - match: main
    tasks: [lint, test]
  - match: "feature/**"
    tasks: [bad, lint]
`

type env struct {
	t      *testing.T
	remote string
	dev    string
}

// newEnv creates a remote and a developer clone whose main branch holds cfg.
func newEnv(t *testing.T, cfg string, extra ...string) *env {
	t.Helper()
	testutil.Isolate(t)
	for _, k := range []string{"GITHUB_TOKEN", "GITHUB_REPOSITORY", "GITHUB_API_URL", "GITHUB_SERVER_URL", "GITHUB_RUN_ID", "GITHUB_EVENT_NAME", "GITHUB_REF", "GITHUB_SHA", "GITHUB_EVENT_PATH"} {
		t.Setenv(k, "")
	}
	e := &env{t: t, remote: testutil.Remote(t)}
	e.dev = testutil.Clone(t, e.remote)
	testutil.WriteFile(t, e.dev, config.FileName, cfg)
	for i := 0; i+1 < len(extra); i += 2 {
		testutil.WriteFile(t, e.dev, extra[i], extra[i+1])
	}
	testutil.CommitAll(t, e.dev, "init")
	testutil.Git(t, e.dev, "push", "--quiet", "-u", "origin", "main")
	return e
}

type result struct {
	code           int
	stdout, stderr string
}

func (e *env) uki(dir string, args ...string) result {
	e.t.Helper()
	e.t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

// ci returns a shallow clone of the default branch, like actions/checkout.
func (e *env) ci() string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "ci")
	testutil.Git(e.t, ".", "clone", "--quiet", "--depth=1", "--branch=main", "file://"+e.remote, dir)
	return dir
}

func (e *env) head(dir string) string { return testutil.Git(e.t, dir, "rev-parse", "HEAD") }

func (e *env) recordsTip() string {
	out := testutil.Git(e.t, ".", "ls-remote", e.remote, "refs/heads/uki/records")
	if out == "" {
		return ""
	}
	return strings.Fields(out)[0]
}

func (e *env) branch(name string) string {
	e.t.Helper()
	testutil.Git(e.t, e.dev, "checkout", "--quiet", "-b", name)
	testutil.WriteFile(e.t, e.dev, name+".txt", name)
	sha := testutil.CommitAll(e.t, e.dev, name)
	testutil.Git(e.t, e.dev, "push", "--quiet", "origin", name)
	return sha
}

func expect(t *testing.T, r result, code int, contains ...string) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	all := r.stdout + r.stderr
	for _, s := range contains {
		if !strings.Contains(all, s) {
			t.Errorf("output lacks %q\nstdout:\n%s\nstderr:\n%s", s, r.stdout, r.stderr)
		}
	}
}

// ---- run / status ----

func TestRunRecordsSuccess(t *testing.T) {
	e := newEnv(t, baseConfig)
	r := e.uki(e.dev, "run")
	expect(t, r, 0, "LINT", "TEST", "uki: recorded")
	if e.recordsTip() == "" {
		t.Fatal("records branch not pushed")
	}
	expect(t, e.uki(e.dev, "status"), 0, "success: Passed: lint, test", "lint  ok", "test  ok")
}

func TestRunSeedsWorkflowAndLockfile(t *testing.T) {
	e := newEnv(t, baseConfig, WorkflowPath, "name: uki\n", ActionsLockPath, "version: 'v0.0.3'\n")
	expect(t, e.uki(e.dev, "run"), 0, "uki: recorded")
	files := testutil.Git(t, e.dev, "ls-tree", "-r", "--name-only", "origin/uki/records", ".github")
	if files != ActionsLockPath+"\n"+WorkflowPath {
		t.Errorf("records branch .github contents:\n%s", files)
	}
}

func TestRunFailureIsRecorded(t *testing.T) {
	e := newEnv(t, baseConfig)
	e.branch("feature/x")
	expect(t, e.uki(e.dev, "run"), 1, "BAD", "FAILED (exit 3)", "LINT", "uki: recorded")
	expect(t, e.uki(e.dev, "status"), 1, "failure: Failed: bad")
}

func TestRunFailFast(t *testing.T) {
	e := newEnv(t, baseConfig)
	e.branch("feature/x")
	r := e.uki(e.dev, "run", "--fail-fast")
	expect(t, r, 1, "BAD")
	if strings.Contains(r.stdout, "LINT") {
		t.Error("--fail-fast still ran lint")
	}
	expect(t, e.uki(e.dev, "status"), 1, "bad", "failure")
}

func TestRunTaskRunnerModeMerges(t *testing.T) {
	e := newEnv(t, baseConfig)
	r := e.uki(e.dev, "run", "lint")
	expect(t, r, 0, "LINT")
	if strings.Contains(r.stdout, "TEST") {
		t.Error("ran a task that was not requested")
	}
	expect(t, e.uki(e.dev, "status"), statusExitPending, "pending: Not run yet: test")
	expect(t, e.uki(e.dev, "run", "test"), 0)
	expect(t, e.uki(e.dev, "status"), 0, "success")
}

func TestRunDirtyDoesNotRecord(t *testing.T) {
	e := newEnv(t, baseConfig)
	testutil.WriteFile(t, e.dev, "untracked.txt", "x")
	expect(t, e.uki(e.dev, "run"), 0, "LINT", "working tree is dirty")
	if e.recordsTip() != "" {
		t.Error("dirty run was recorded")
	}
}

func TestRunNoRecord(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run", "--no-record"), 0, "LINT")
	if e.recordsTip() != "" {
		t.Error("--no-record run was recorded")
	}
}

func TestRunTaskThatDirtiesTreeIsNotRecorded(t *testing.T) {
	e := newEnv(t, `
tasks: { gen: { run: "touch generated.txt" } }
branches: [ { match: "**", tasks: [gen] } ]
`)
	expect(t, e.uki(e.dev, "run"), 0, "left the working tree dirty")
	if e.recordsTip() != "" {
		t.Error("recorded although the task dirtied the tree")
	}
}

func TestRunHeadMovedIsNotRecorded(t *testing.T) {
	e := newEnv(t, `
tasks: { move: { run: "git commit --quiet --allow-empty -m moved" } }
branches: [ { match: "**", tasks: [move] } ]
`)
	expect(t, e.uki(e.dev, "run"), 0, "HEAD changed")
	if e.recordsTip() != "" {
		t.Error("recorded although HEAD moved")
	}
}

func TestRunErrors(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run", "nope"), 1, `unknown task "nope"`)
	expect(t, e.uki(e.dev, "run", "--branch", "other"), 1, `no branch rule matches "other"`)
	testutil.Git(t, e.dev, "checkout", "--quiet", "--detach")
	expect(t, e.uki(e.dev, "run"), 1, "detached HEAD")
	// Explicit tasks work on a detached HEAD.
	expect(t, e.uki(e.dev, "run", "lint"), 0, "uki: recorded")
}

func TestStatusNoRecord(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "status"), statusExitPending, "record  none", "No uki record")
}

func TestStatusRevAndBranchFlags(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	testutil.CommitAll(t, e.dev, "next")
	expect(t, e.uki(e.dev, "status"), statusExitPending, "record  none")
	expect(t, e.uki(e.dev, "status", "--rev", "HEAD~1"), 0, "success")
	// Same record judged by the feature rules: bad was never run.
	expect(t, e.uki(e.dev, "status", "--rev", "HEAD~1", "--branch", "feature/x"), statusExitPending, "Not run yet: bad")
}

func TestStatusWarnings(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	testutil.Git(t, e.dev, "remote", "set-head", "origin", "main")
	testutil.WriteFile(t, e.dev, config.FileName, baseConfig+"# local edit\n")
	expect(t, e.uki(e.dev, "status"), 0, "working tree is dirty", "differs from refs/remotes/origin/main")
}

// ---- mark ----

func TestMarkBranchMode(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	mainSHA := e.head(e.dev)
	featSHA := e.branch("feature/x")
	expect(t, e.uki(e.dev, "run"), 1)
	noRecSHA := e.branch("feature/y")

	ci := e.ci()
	expect(t, e.uki(ci, "mark", "--dry-run", "--sha", mainSHA, "--branch", "main"), 0, "success Passed: lint, test")
	expect(t, e.uki(ci, "mark", "--dry-run", "--sha", featSHA, "--branch", "feature/x"), 0, "failure Failed: bad")
	expect(t, e.uki(ci, "mark", "--dry-run", "--sha", noRecSHA, "--branch", "feature/y"), 0, "pending No uki record")
}

func TestMarkUsesDefaultBranchRules(t *testing.T) {
	e := newEnv(t, baseConfig)
	// A feature branch tries to drop its failing requirement.
	testutil.Git(t, e.dev, "checkout", "--quiet", "-b", "feature/sneaky")
	testutil.WriteFile(t, e.dev, config.FileName, strings.Replace(baseConfig, "[bad, lint]", "[lint]", 1))
	sha := testutil.CommitAll(t, e.dev, "loosen rules")
	testutil.Git(t, e.dev, "push", "--quiet", "origin", "feature/sneaky")
	expect(t, e.uki(e.dev, "run"), 0)

	expect(t, e.uki(e.ci(), "mark", "--dry-run", "--sha", sha, "--branch", "feature/sneaky"), 0, "pending Not run yet: bad")
}

func TestUnmatchedBranchIsPending(t *testing.T) {
	e := newEnv(t, baseConfig) // rules for main and feature/** only
	sha := e.branch("chore/x")
	expect(t, e.uki(e.dev, "run", "lint"), 0, "uki: recorded")
	expect(t, e.uki(e.ci(), "mark", "--dry-run", "--sha", sha, "--branch", "chore/x"), 0,
		"pending No branch rule in uki.yaml matches this branch")
	expect(t, e.uki(e.dev, "status"), statusExitPending, "No branch rule")
}

func TestEmptyTaskListIsSuccess(t *testing.T) {
	e := newEnv(t, `
tasks: { lint: { run: "true" } }
branches: [ { match: "docs/**", tasks: [] }, { match: "**", tasks: [lint] } ]
`)
	sha := e.branch("docs/readme")
	expect(t, e.uki(e.dev, "run", "lint"), 0, "uki: recorded")
	expect(t, e.uki(e.ci(), "mark", "--dry-run", "--sha", sha, "--branch", "docs/readme"), 0, "success The branch rule requires no tasks")
}

// pushEventEnv sets up the environment GitHub Actions provides for a push.
func pushEventEnv(t *testing.T, ref, sha, before string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	testutil.WriteFile(t, filepath.Dir(path), filepath.Base(path), `{"ref":"`+ref+`","before":"`+before+`","after":"`+sha+`"}`)
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("GITHUB_REF", ref)
	t.Setenv("GITHUB_SHA", sha)
	t.Setenv("GITHUB_EVENT_PATH", path)
}

func TestMarkGitHubEventUsesConfiguredRecordsBranch(t *testing.T) {
	e := newEnv(t, baseConfig+"record: { branch: ci/records }\n")
	expect(t, e.uki(e.dev, "run"), 0, "recorded "+short(e.head(e.dev))+" to origin/ci/records")
	mainSHA := e.head(e.dev)
	tip := strings.Fields(testutil.Git(t, ".", "ls-remote", e.remote, "refs/heads/ci/records"))[0]
	ci := e.ci()

	// Push to a work branch: the head commit is judged.
	pushEventEnv(t, "refs/heads/main", mainSHA, strings.Repeat("0", 40))
	expect(t, e.uki(ci, "mark", "--dry-run", "--github-event"), 0, short(mainSHA)+" main success")

	// Push to the records branch named in uki.yaml: changed records are judged.
	pushEventEnv(t, "refs/heads/ci/records", tip, strings.Repeat("0", 40))
	expect(t, e.uki(ci, "mark", "--dry-run", "--github-event"), 0, short(mainSHA)+" main success")

	// "uki/records" is just an ordinary branch here.
	pushEventEnv(t, "refs/heads/uki/records", tip, strings.Repeat("0", 40))
	r := e.uki(ci, "mark", "--dry-run", "--github-event")
	expect(t, r, 0, "uki/records pending No branch rule")

	// Tags are ignored.
	pushEventEnv(t, "refs/tags/v1.0.0", mainSHA, strings.Repeat("0", 40))
	expect(t, e.uki(ci, "mark", "--dry-run", "--github-event"), 0, "refs/tags/v1.0.0 is not a branch")
}

func TestMarkGitHubEventErrors(t *testing.T) {
	e := newEnv(t, baseConfig)
	ci := e.ci()
	sha := e.head(ci)
	pushEventEnv(t, "refs/heads/main", sha, "")
	expect(t, e.uki(ci, "mark", "--dry-run", "--github-event", "--sha", sha), 1, "cannot be combined")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	expect(t, e.uki(ci, "mark", "--dry-run", "--github-event"), 1, "push events only")
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("GITHUB_SHA", "")
	expect(t, e.uki(ci, "mark", "--dry-run", "--github-event"), 1, "needs GITHUB_REF and GITHUB_SHA")
}

func TestMarkSkipsCommitNotOnRemote(t *testing.T) {
	e := newEnv(t, baseConfig)
	sha := testutil.CommitAll(t, e.dev, "local only")
	r := e.uki(e.ci(), "mark", "--dry-run", "--sha", sha, "--branch", "main")
	expect(t, r, 0, "not on the remote yet")
	if r.stdout != "" {
		t.Errorf("printed a verdict: %s", r.stdout)
	}
}

func TestMarkRecordsMode(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	mainSHA := e.head(e.dev)
	first := e.recordsTip()

	ci := e.ci()
	r := e.uki(ci, "mark", "--dry-run", "--records-before", strings.Repeat("0", 40), "--records-after", first)
	expect(t, r, 0, short(mainSHA)+" main success")

	featSHA := e.branch("feature/x")
	expect(t, e.uki(e.dev, "run"), 1)
	r = e.uki(ci, "mark", "--dry-run", "--records-before", first, "--records-after", e.recordsTip())
	expect(t, r, 0, short(featSHA)+" feature/x failure")
	if strings.Contains(r.stdout, short(mainSHA)) {
		t.Errorf("unchanged record was re-marked:\n%s", r.stdout)
	}
}

func TestMarkIgnoresUnexpectedRecordFiles(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	before := e.recordsTip()
	pushToRecords(t, e, func(wt string) {
		testutil.WriteFile(t, wt, "records/x/a.json", "{}")
		testutil.WriteFile(t, wt, "records/zz/"+strings.Repeat("a", 40)+".json", "{}") // wrong shard
	})
	r := e.uki(e.ci(), "mark", "--dry-run", "--records-before", before, "--records-after", e.recordsTip())
	expect(t, r, 0, `ignoring unexpected file "records/x/a.json"`, "ignoring unexpected file \"records/zz/")
}

func TestMarkArgumentErrors(t *testing.T) {
	e := newEnv(t, baseConfig)
	ci := e.ci()
	expect(t, e.uki(ci, "mark", "--dry-run", "--sha", "HEAD"), 1, "must be a full commit SHA")
	expect(t, e.uki(ci, "mark", "--dry-run"), 1, "pass --github-event")
	expect(t, e.uki(ci, "mark", "--sha", e.head(ci), "--branch", "main"), 1, "GITHUB_TOKEN")
	expect(t, e.uki(ci, "mark", "--dry-run", "--records-after", e.head(ci)), 1, "does not exist")
}

func TestMarkPostsStatus(t *testing.T) {
	e := newEnv(t, baseConfig)
	expect(t, e.uki(e.dev, "run"), 0)
	sha := e.head(e.dev)

	var got []github.Status
	var paths []string
	code := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s github.Status
		_ = json.NewDecoder(r.Body).Decode(&s)
		got = append(got, s)
		paths = append(paths, r.URL.Path)
		w.WriteHeader(code)
	}))
	defer srv.Close()
	t.Setenv("GITHUB_TOKEN", "tkn")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_SERVER_URL", "https://github.example")
	t.Setenv("GITHUB_RUN_ID", "42")

	ci := e.ci()
	expect(t, e.uki(ci, "mark", "--sha", sha, "--branch", "main"), 0)
	if len(got) != 1 {
		t.Fatalf("got %d requests", len(got))
	}
	want := github.Status{State: "success", Context: "uki", Description: "Passed: lint, test", TargetURL: "https://github.example/o/r/actions/runs/42"}
	if got[0] != want || paths[0] != "/repos/o/r/statuses/"+sha {
		t.Errorf("request = %s %+v", paths[0], got[0])
	}

	code = http.StatusUnprocessableEntity
	expect(t, e.uki(ci, "mark", "--sha", sha, "--branch", "main"), 0, "GitHub does not know")
}

// ---- signatures ----

func TestMarkVerifiesSignatures(t *testing.T) {
	good, goodPub := testutil.SSHKey(t)
	evil, _ := testutil.SSHKey(t)
	e := newEnv(t, baseConfig+`
record:
  sign: true
  allowed_signers: .uki/allowed_signers
`, ".uki/allowed_signers", "test@example.com "+goodPub+"\n")
	sha := e.head(e.dev)
	mark := func() result {
		return e.uki(e.ci(), "mark", "--dry-run", "--sha", sha, "--branch", "main")
	}

	testutil.UseSSHSigning(t, e.dev, good)
	expect(t, e.uki(e.dev, "run"), 0, "uki: recorded")
	expect(t, mark(), 0, "success")
	expect(t, e.uki(e.dev, "status"), 0, "success")

	testutil.UseSSHSigning(t, e.dev, evil)
	expect(t, e.uki(e.dev, "run"), 0, "uki: recorded")
	expect(t, mark(), 0, "error   Record signature could not be verified", `signature status "U"`)

	// An unsigned commit touching the record.
	pushToRecords(t, e, func(wt string) {
		p := filepath.Join(wt, record.Path(sha))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		testutil.WriteFile(t, wt, record.Path(sha), string(b)+"\n")
	})
	expect(t, mark(), 0, "error", "is not SSH-signed")

	// A commit carrying a PGP signature block is rejected before git verifies it,
	// since verify-commit would accept any key in the runner's keyring.
	pushPGPSignedRecordCommit(t, e, sha)
	expect(t, mark(), 0, "error", "is not SSH-signed")
}

// pushToRecords commits edit's changes to the records branch without signing.
func pushToRecords(t *testing.T, e *env, edit func(wt string)) {
	t.Helper()
	testutil.Git(t, e.dev, "fetch", "--quiet", "origin", "uki/records")
	wt := filepath.Join(t.TempDir(), "wt")
	testutil.Git(t, e.dev, "worktree", "add", "--quiet", "--detach", wt, "FETCH_HEAD")
	defer testutil.Git(t, e.dev, "worktree", "remove", "--force", wt)
	edit(wt)
	testutil.Git(t, wt, "add", "-A")
	testutil.Git(t, wt, "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "tamper")
	testutil.Git(t, wt, "push", "--quiet", "origin", "HEAD:refs/heads/uki/records")
}

// pushPGPSignedRecordCommit pushes a record-touching commit whose header
// carries a (bogus) PGP signature block.
func pushPGPSignedRecordCommit(t *testing.T, e *env, sha string) {
	t.Helper()
	var unsigned string
	pushToRecords(t, e, func(wt string) {
		testutil.WriteFile(t, wt, record.Path(sha), `{"sha":"`+sha+`"}`)
	})
	unsigned = testutil.Git(t, e.dev, "ls-remote", e.remote, "refs/heads/uki/records")
	unsigned = strings.Fields(unsigned)[0]
	testutil.Git(t, e.dev, "fetch", "--quiet", "origin", "uki/records")

	raw := testutil.Git(t, e.dev, "cat-file", "commit", unsigned)
	hdr, body, _ := strings.Cut(raw, "\n\n")
	signed := hdr + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n -----END PGP SIGNATURE-----\n\n" + body + "\n"

	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = e.dev
	cmd.Stdin = strings.NewReader(signed)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, out)
	}
	c := strings.TrimSpace(string(out))
	testutil.Git(t, e.dev, "push", "--quiet", "--force", "origin", c+":refs/heads/uki/records")
}

// ---- init ----

func TestInitWritesTemplates(t *testing.T) {
	testutil.Isolate(t)
	dir := testutil.Clone(t, testutil.Remote(t))
	e := &env{t: t}
	expect(t, e.uki(dir, "init"), 0, "create uki.yaml", "create "+WorkflowPath)
	b, err := os.ReadFile(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Parse(b); err != nil {
		t.Errorf("generated uki.yaml does not parse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, WorkflowPath)); err != nil {
		t.Error(err)
	}
	expect(t, e.uki(dir, "init"), 0, "skip   uki.yaml (exists)")
}

func TestMainUsage(t *testing.T) {
	e := &env{t: t}
	dir := t.TempDir()
	expect(t, e.uki(dir), 2, "Usage:")
	expect(t, e.uki(dir, "bogus"), 2, `unknown command "bogus"`)
	expect(t, e.uki(dir, "version"), 0)
	for _, cmd := range []string{"run", "status", "mark", "init"} {
		r := e.uki(dir, cmd, "-h")
		expect(t, r, 0, "Usage of uki "+cmd)
		if strings.Contains(r.stderr, "help requested") {
			t.Errorf("%s -h printed an error: %s", cmd, r.stderr)
		}
		expect(t, e.uki(dir, cmd, "--no-such-flag"), 2, "flag provided but not defined")
	}
}

func TestConfigGet(t *testing.T) {
	e := newEnv(t, baseConfig+"record: { branch: ci/records, sign: true }\n")
	for key, want := range map[string]string{
		"record.branch":          "ci/records",
		"record.remote":          "origin", // default applied
		"record.sign":            "true",
		"record.allowed_signers": "",
		"status.context":         "uki",
	} {
		r := e.uki(e.dev, "config", "get", key)
		if r.code != 0 || r.stdout != want+"\n" {
			t.Errorf("config get %s = %q (exit %d, stderr %q), want %q", key, r.stdout, r.code, r.stderr, want)
		}
	}
	expect(t, e.uki(e.dev, "config", "get", "nope"), 1, `unknown config key "nope"`, "record.branch")
	expect(t, e.uki(e.dev, "config"), 2, "Usage: uki config get <key>")
	expect(t, e.uki(e.dev, "config", "set", "record.branch"), 2, "Usage: uki config get <key>")
	expect(t, e.uki(e.dev, "config", "-h"), 0, "Keys: record.allowed_signers, record.branch")
}

// ---- helpers under test ----

func TestLatestBranch(t *testing.T) {
	now := time.Now()
	r := &record.Record{Tasks: map[string]record.TaskResult{
		"a": {Branch: "old", StartedAt: now.Add(-time.Hour)},
		"b": {Branch: "", StartedAt: now}, // detached HEAD run
		"c": {Branch: "mid", StartedAt: now.Add(-time.Minute)},
	}}
	if got := latestBranch(r); got != "" {
		t.Errorf("latestBranch = %q, want the newest entry's branch (empty)", got)
	}
	delete(r.Tasks, "b")
	if got := latestBranch(r); got != "mid" {
		t.Errorf("latestBranch = %q, want mid", got)
	}
}
