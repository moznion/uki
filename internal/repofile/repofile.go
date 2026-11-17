// Package repofile writes files that end up tracked in a git repository.
package repofile

import (
	"os"
	"path/filepath"
)

// Write creates parent directories and writes b to path.
//
// Repository files use the conventional 0644/0755 modes: git records only the
// executable bit and these files are meant to be shared, so the tighter modes
// gosec suggests (G301/G306) would only diverge from what a checkout produces.
func Write(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // see above
		return err
	}
	return os.WriteFile(path, b, 0o644) //nolint:gosec // see above
}
