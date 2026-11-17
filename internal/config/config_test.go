package config

import (
	"reflect"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"main", "main", true},
		{"main", "main2", false},
		{"feature/*", "feature/foo", true},
		{"feature/*", "feature/foo/bar", false},
		{"feature/**", "feature/foo/bar", true},
		{"release-?", "release-1", true},
		{"release-?", "release-10", false},
		{"**", "anything/at/all", true},
		{"fix.*", "fixx", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestParseAndTasksForBranch(t *testing.T) {
	c, err := Parse([]byte(`
tasks:
  lint: { run: "echo lint" }
  test: { run: "echo test" }
branches:
  - match: main
    tasks: [lint, test]
  - match: "**"
    tasks: [lint]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Record.Branch != "uki/records" || c.Record.Remote != "origin" || c.Status.Context != "uki" {
		t.Errorf("defaults not applied: %+v", c)
	}
	got, ok := c.TasksForBranch("main")
	if !ok || !reflect.DeepEqual(got, []string{"lint", "test"}) {
		t.Errorf("main: got %v %v", got, ok)
	}
	got, ok = c.TasksForBranch("feature/x")
	if !ok || !reflect.DeepEqual(got, []string{"lint"}) {
		t.Errorf("feature/x: got %v %v", got, ok)
	}
}

func TestParseRejectsUnknownTask(t *testing.T) {
	_, err := Parse([]byte(`
tasks:
  lint: { run: "echo lint" }
branches:
  - match: main
    tasks: [test]
`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`
taks:
  lint: { run: "echo lint" }
`))
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
	_, err = Parse([]byte(`
tasks:
  lint: { run: "echo lint", evn: { A: "1" } }
`))
	if err == nil {
		t.Fatal("expected error for unknown nested field")
	}
}

func TestParseEmpty(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Record.Branch != "uki/records" {
		t.Errorf("default branch not applied: %q", c.Record.Branch)
	}
}

func TestParseArtifacts(t *testing.T) {
	c, err := Parse([]byte(`
tasks:
  test: { run: "go test", artifacts: [coverage.out, build/report.xml] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Tasks["test"].Artifacts, []string{"coverage.out", "build/report.xml"}) {
		t.Errorf("artifacts = %v", c.Tasks["test"].Artifacts)
	}
	if c.Record.MaxArtifactBytes != DefaultMaxArtifactBytes {
		t.Errorf("default max_artifact_bytes not applied: %d", c.Record.MaxArtifactBytes)
	}

	for name, cfg := range map[string]string{
		"absolute":      `tasks: { t: { run: x, artifacts: [/etc/passwd] } }`,
		"parent":        `tasks: { t: { run: x, artifacts: [../x] } }`,
		"unclean":       `tasks: { t: { run: x, artifacts: [a//b] } }`,
		"dot":           `tasks: { t: { run: x, artifacts: [./a] } }`,
		"git dir":       `tasks: { t: { run: x, artifacts: [.git/config] } }`,
		"backslash":     `tasks: { t: { run: x, artifacts: ['a\b'] } }`,
		"empty":         `tasks: { t: { run: x, artifacts: [""] } }`,
		"duplicate":     `tasks: { t: { run: x, artifacts: [a, a] } }`,
		"task name":     `tasks: { a/b: { run: x, artifacts: [a] } }`,
		"negative size": `record: { max_artifact_bytes: -1 }`,
	} {
		if _, err := Parse([]byte(cfg)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// A task without artifacts may still use any name.
	if _, err := Parse([]byte(`tasks: { a/b: { run: x } }`)); err != nil {
		t.Errorf("task name without artifacts: %v", err)
	}
}

func TestParseInputs(t *testing.T) {
	c, err := Parse([]byte(`
inputs: [go.mod]
tasks:
  test: { run: x, inputs: ["src/**"] }
  vuln: { run: y }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.TaskInputs("test"); !reflect.DeepEqual(got, []string{"go.mod", "src/**"}) {
		t.Errorf("TaskInputs(test) = %v", got)
	}
	if got := c.TaskInputs("vuln"); got != nil {
		t.Errorf("a task without inputs must not get the top-level ones: %v", got)
	}
	for _, cfg := range []string{
		`inputs: [../x]`,
		`inputs: [/abs]`,
		`tasks: { t: { run: x, inputs: ["./src/**"] } }`,
		`tasks: { t: { run: x, inputs: [""] } }`,
		`irrelevant: [../docs]`,
	} {
		if _, err := Parse([]byte(cfg)); err == nil {
			t.Errorf("%s: expected an error", cfg)
		}
	}
}

func TestInputMatcher(t *testing.T) {
	m := InputMatcher([]string{"src/**", "go.mod", "cmd/*.go"})
	for p, want := range map[string]bool{
		"src/a/b.go": true, "go.mod": true, "cmd/main.go": true,
		"cmd/sub/main.go": false, "go.sum": false, "srcx/a": false,
	} {
		if m(p) != want {
			t.Errorf("match(%q) = %v, want %v", p, !want, want)
		}
	}
}
