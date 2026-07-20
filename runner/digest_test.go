package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jbofill10/looper/config"
)

func TestScript_CapturesDigest(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{
		Name: "plan",
		Type: config.StepScript,
		Run: `
			echo "hi" > "$(pwd)/plan-digest.md"
			echo "PLAN_DIGEST_FILE=$(pwd)/plan-digest.md" >> "$LOOPER_OUTPUT"
		`,
		Digest: "PLAN_DIGEST_FILE",
	}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(rc.StepsDir(), "plan.digest.md"))
	if err != nil {
		t.Fatalf("read captured digest: %v", err)
	}
	if string(data) != "hi\n" {
		t.Errorf("digest content = %q, want %q", data, "hi\n")
	}
	if _, ok := rc.Get("PLAN_DIGEST_FILE"); !ok {
		t.Errorf("PLAN_DIGEST_FILE should be captured as an implicit output")
	}
}

func TestScript_NoDigestFieldCapturesNothing(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "plain", Type: config.StepScript, Run: "true"}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rc.StepsDir(), "plain.digest.md")); !os.IsNotExist(err) {
		t.Errorf("expected no digest file, got err=%v", err)
	}
}

func TestScript_DigestVarUnsetIsNotError(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "x", Type: config.StepScript, Run: "true", Digest: "MISSING_VAR"}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rc.StepsDir(), "x.digest.md")); !os.IsNotExist(err) {
		t.Errorf("expected no digest file, got err=%v", err)
	}
}
