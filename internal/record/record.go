// Package record defines the per-commit record stored on the records branch.
package record

import (
	"encoding/json"
	"fmt"
	"time"
)

// Record collects every task result recorded for one commit.
// It is stored at Path(SHA) on the records branch.
type Record struct {
	SHA   string                `json:"sha"`
	Tree  string                `json:"tree"`
	Tasks map[string]TaskResult `json:"tasks"`
}

type TaskResult struct {
	Command  string `json:"command"`
	Success  bool   `json:"success"`
	ExitCode int    `json:"exit_code"`
	// Branch is the local branch the task ran on (informational).
	Branch string `json:"branch,omitempty"`
	// ConfigSHA256 is the hash of uki.yaml at the target commit.
	ConfigSHA256 string    `json:"config_sha256"`
	StartedAt    time.Time `json:"started_at"`
	DurationMS   int64     `json:"duration_ms"`
	// LogSHA256 is the hash of the combined stdout/stderr.
	LogSHA256  string `json:"log_sha256"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	UkiVersion string `json:"uki_version"`
}

// ValidSHA reports whether s is a full lowercase SHA-1 or SHA-256 object name.
func ValidSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Path shards records by the first two hex chars to keep directories small.
// sha must satisfy ValidSHA.
func Path(sha string) string {
	return fmt.Sprintf("records/%s/%s.json", sha[:2], sha)
}

func Parse(b []byte) (*Record, error) {
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Record) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Merge overlays results onto existing (which may be nil). Later results win.
func Merge(existing *Record, sha, tree string, results map[string]TaskResult) *Record {
	r := &Record{SHA: sha, Tree: tree, Tasks: map[string]TaskResult{}}
	if existing != nil && existing.SHA == sha {
		for k, v := range existing.Tasks {
			r.Tasks[k] = v
		}
	}
	for k, v := range results {
		r.Tasks[k] = v
	}
	return r
}
