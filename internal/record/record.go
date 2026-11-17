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
	// Artifacts maps each stored artifact (its repository-relative path) to
	// its digest. The content lives at ArtifactPath on the records branch.
	Artifacts map[string]Artifact `json:"artifacts,omitempty"`
	// InputsSHA256 is the inputs key of the task at this commit (see package
	// inputs); empty when the task declares no inputs.
	InputsSHA256 string `json:"inputs_sha256,omitempty"`
	// ReusedFrom is set when the task was not run for this commit: the result
	// (command, outcome, log digest, artifacts, ...) is the one recorded for
	// the commit named here, which had the same inputs. It always names the
	// commit the task actually ran on, never another reuse.
	ReusedFrom string `json:"reused_from,omitempty"`
}

// IndexEntry is stored at IndexPath(key): the latest commit whose passing,
// actually-run result of Task had that inputs key.
type IndexEntry struct {
	SHA  string `json:"sha"`
	Task string `json:"task"`
}

// IndexPath shards the inputs index like records. key must satisfy ValidKey.
func IndexPath(key string) string {
	return fmt.Sprintf("inputs/%s/%s.json", key[:2], key)
}

// ValidKey reports whether s is a lowercase hex SHA-256 digest.
func ValidKey(s string) bool {
	return len(s) == 64 && ValidSHA(s)
}

// Artifact describes one file a task produced, as stored on the records branch.
type Artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
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

// ArtifactDir is the directory holding every artifact of task for sha.
// sha must satisfy ValidSHA and task config.ValidArtifactTaskName.
func ArtifactDir(sha, task string) string {
	return fmt.Sprintf("artifacts/%s/%s/%s", sha[:2], sha, task)
}

// ArtifactPath is where the artifact at the repository-relative path p of
// task is stored for sha.
func ArtifactPath(sha, task, p string) string {
	return ArtifactDir(sha, task) + "/" + p
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
