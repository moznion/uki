package verify

import (
	"errors"
	"testing"

	"github.com/moznion/uki/internal/record"
)

func TestEvaluate(t *testing.T) {
	rec := func(tasks map[string]record.TaskResult) *record.Record {
		return &record.Record{SHA: "s", Tree: "t", Tasks: tasks}
	}
	ok := record.TaskResult{Success: true, ConfigSHA256: "c"}
	ng := record.TaskResult{Success: false, ConfigSHA256: "c"}
	stale := record.TaskResult{Success: true, ConfigSHA256: "old"}
	base := Input{SHA: "s", Tree: "t", ConfigSHA256: "c", Required: []string{"lint", "test"}}

	cases := []struct {
		name string
		mod  func(in *Input)
		want string
	}{
		{"no record", func(in *Input) {}, StatePending},
		{"all passed", func(in *Input) { in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": ok}) }, StateSuccess},
		{"one failed", func(in *Input) { in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": ng}) }, StateFailure},
		{"failure wins over missing", func(in *Input) { in.Record = rec(map[string]record.TaskResult{"test": ng}) }, StateFailure},
		{"missing", func(in *Input) { in.Record = rec(map[string]record.TaskResult{"lint": ok}) }, StatePending},
		{"stale config", func(in *Input) { in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": stale}) }, StateError},
		{"invalid reuse", func(in *Input) {
			in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": ok})
			in.InvalidReuse = map[string]bool{"test": true}
		}, StateError},
		{"failure wins over invalid reuse", func(in *Input) {
			in.Record = rec(map[string]record.TaskResult{"lint": ng, "test": ok})
			in.InvalidReuse = map[string]bool{"test": true}
		}, StateFailure},
		{"tree mismatch", func(in *Input) {
			in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": ok})
			in.Record.Tree = "other"
		}, StateError},
		{"bad signature", func(in *Input) {
			in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": ok})
			in.SignatureErr = errors.New("bad")
		}, StateError},
		{"no matching rule", func(in *Input) {
			in.Record = rec(map[string]record.TaskResult{"lint": ok, "test": ok})
			in.Required = nil
			in.NoMatchingRule = true
		}, StatePending},
		{"no matching rule without record", func(in *Input) { in.NoMatchingRule = true }, StatePending},
		{"rule with empty task list", func(in *Input) {
			in.Record = rec(nil)
			in.Required = nil
		}, StateSuccess},
	}
	for _, c := range cases {
		in := base
		c.mod(&in)
		if got := Evaluate(in); got.State != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got.State, got.Description, c.want)
		}
	}
}
