// Package verify decides the commit status for a commit from its record.
package verify

import (
	"fmt"
	"strings"

	"github.com/moznion/uki/internal/record"
)

const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
)

type Input struct {
	SHA          string
	Tree         string // tree of SHA as seen by GitHub
	ConfigSHA256 string // hash of uki.yaml at SHA
	Required     []string
	// NoMatchingRule is set when no branch rule matches the branch. Nothing
	// can satisfy such a branch, so it stays pending.
	NoMatchingRule bool
	Record         *record.Record // nil when no record exists
	SignatureErr   error          // non-nil when signature verification failed
}

type Verdict struct {
	State       string
	Description string
}

func Evaluate(in Input) Verdict {
	if in.NoMatchingRule {
		return Verdict{StatePending, "No branch rule in uki.yaml matches this branch"}
	}
	if in.Record == nil {
		return Verdict{StatePending, "No uki record for this commit yet"}
	}
	if in.SignatureErr != nil {
		return Verdict{StateError, "Record signature could not be verified"}
	}
	if in.Record.SHA != in.SHA || in.Record.Tree != in.Tree {
		return Verdict{StateError, "Record does not match this commit"}
	}
	if len(in.Required) == 0 {
		return Verdict{StateSuccess, "The branch rule requires no tasks"}
	}

	var failed, missing, stale []string
	for _, name := range in.Required {
		res, ok := in.Record.Tasks[name]
		switch {
		case !ok:
			missing = append(missing, name)
		case res.ConfigSHA256 != in.ConfigSHA256:
			stale = append(stale, name)
		case !res.Success:
			failed = append(failed, name)
		}
	}
	switch {
	case len(failed) > 0:
		return Verdict{StateFailure, describe("Failed", failed)}
	case len(stale) > 0:
		return Verdict{StateError, describe("Ran with a different uki.yaml", stale)}
	case len(missing) > 0:
		return Verdict{StatePending, describe("Not run yet", missing)}
	}
	return Verdict{StateSuccess, describe("Passed", in.Required)}
}

// describe keeps within GitHub's 140-char description limit.
func describe(prefix string, names []string) string {
	s := fmt.Sprintf("%s: %s", prefix, strings.Join(names, ", "))
	if len(s) > 140 {
		s = s[:137] + "..."
	}
	return s
}
