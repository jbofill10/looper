package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// captureDigest copies the content of the file at step's digest output var
// (if step.Digest is set and that var resolved to an existing file) into
// steps/<step.Name>.digest.md in rc's run dir. A missing var or missing
// file is not an error — the step simply has no digest for this iteration.
func captureDigest(rc *runctx.RunContext, step config.Step) error {
	if step.Digest == "" {
		return nil
	}
	path, ok := rc.Get(step.Digest)
	if !ok || path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read digest file: %w", err)
	}
	dest := filepath.Join(rc.StepsDir(), step.Name+".digest.md")
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("write digest: %w", err)
	}
	return nil
}
