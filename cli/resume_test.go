package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbofill10/looper/runctx"
)

// touchIterDir creates a single-worker-style iteration dir at
// <root>/<id> with a context.json, and, if progress is non-nil, a
// progress.json. mtime backdates both files so tests can control
// "most recent" ordering deterministically.
func touchIterDir(t *testing.T, root, id string, progress *runctx.Progress, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, id)
	rc, err := runctx.New(dir)
	if err != nil {
		t.Fatalf("runctx.New: %v", err)
	}
	rc.Set("TASK_ID", "1")
	if err := rc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if progress != nil {
		if err := rc.SaveProgress(*progress); err != nil {
			t.Fatalf("SaveProgress: %v", err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, "context.json"), mtime, mtime); err != nil {
		t.Fatalf("Chtimes context.json: %v", err)
	}
	if progress != nil {
		if err := os.Chtimes(filepath.Join(dir, "progress.json"), mtime, mtime); err != nil {
			t.Fatalf("Chtimes progress.json: %v", err)
		}
	}
	return dir
}

func TestFindResumeDir_PicksLatestIncomplete(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runs", "l")
	now := time.Now()

	// Oldest: done, should be ignored regardless of recency.
	touchIterDir(t, root, "iter-1", &runctx.Progress{Completed: []string{"a"}, Done: true}, now.Add(-3*time.Hour))
	// Middle: incomplete, older than the next one.
	touchIterDir(t, root, "iter-2", &runctx.Progress{Completed: []string{"a"}, Done: false}, now.Add(-2*time.Hour))
	// Most recent: incomplete -> should be picked.
	want := touchIterDir(t, root, "iter-3", &runctx.Progress{Completed: []string{}, Done: false}, now.Add(-1*time.Hour))
	// Newest of all, but Done -> must be ignored even though it's newest.
	touchIterDir(t, root, "iter-4", &runctx.Progress{Completed: []string{"a"}, Done: true}, now)

	got, ok := findResumeDir(base, "l")
	if !ok {
		t.Fatalf("findResumeDir: expected ok=true")
	}
	if got != want {
		t.Errorf("findResumeDir = %q, want %q", got, want)
	}
}

func TestFindResumeDir_MissingProgressTreatedAsIncomplete(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runs", "l")
	now := time.Now()
	want := touchIterDir(t, root, "iter-1", nil, now)

	got, ok := findResumeDir(base, "l")
	if !ok {
		t.Fatalf("findResumeDir: expected ok=true")
	}
	if got != want {
		t.Errorf("findResumeDir = %q, want %q", got, want)
	}
}

func TestFindResumeDir_WorkerSubdirs(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runs", "l")
	now := time.Now()
	touchIterDir(t, filepath.Join(root, "w1"), "iter-1", &runctx.Progress{Done: true}, now.Add(-time.Hour))
	want := touchIterDir(t, filepath.Join(root, "w2"), "iter-1", &runctx.Progress{Done: false}, now)

	got, ok := findResumeDir(base, "l")
	if !ok {
		t.Fatalf("findResumeDir: expected ok=true")
	}
	if got != want {
		t.Errorf("findResumeDir = %q, want %q", got, want)
	}
}

func TestFindResumeDir_NoneFound(t *testing.T) {
	base := t.TempDir()
	if _, ok := findResumeDir(base, "nonexistent"); ok {
		t.Errorf("expected ok=false for a loop with no runs dir")
	}
}

func TestFindResumeDir_AllDone(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runs", "l")
	touchIterDir(t, root, "iter-1", &runctx.Progress{Completed: []string{"a"}, Done: true}, time.Now())
	if _, ok := findResumeDir(base, "l"); ok {
		t.Errorf("expected ok=false when all iterations are Done")
	}
}

// TestRunLoop_ResumeDirSkipsCompletedStep exercises RunLoop end to end with
// ResumeDir set: a 2-step script loop with get-task pre-marked Completed
// should only run the "work" step.
func TestRunLoop_ResumeDirSkipsCompletedStep(t *testing.T) {
	repo := t.TempDir()
	loopDir := filepath.Join(repo, ".looper", "loops")
	if err := os.MkdirAll(loopDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sink := filepath.Join(repo, "sink.txt")
	loopYAML := "name: r\nmax_iterations: 1\nsteps:\n" +
		"  - name: get-task\n    type: script\n    run: \"echo GOT >> " + sink + "\"\n" +
		"  - name: work\n    type: script\n    run: \"echo WORKED >> " + sink + "\"\n"
	if err := os.WriteFile(filepath.Join(loopDir, "r.yaml"), []byte(loopYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	baseDir := filepath.Join(repo, ".looper")
	resumeDir := filepath.Join(baseDir, "runs", "r", "iter-old")
	rc, err := runctx.New(resumeDir)
	if err != nil {
		t.Fatalf("runctx.New: %v", err)
	}
	if err := rc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rc.SaveProgress(runctx.Progress{Completed: []string{"get-task"}, Done: false}); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}

	err = RunLoop(RunOptions{
		LoopName:  "r",
		BaseDir:   baseDir,
		In:        strings.NewReader(""),
		Out:       &strings.Builder{},
		ResumeDir: resumeDir,
	})
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}

	data, err := os.ReadFile(sink)
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	if strings.Contains(string(data), "GOT") {
		t.Errorf("get-task should have been skipped as already completed; sink = %q", data)
	}
	if !strings.Contains(string(data), "WORKED") {
		t.Errorf("work should have run; sink = %q", data)
	}
}

// TestResumeCLI_NothingToResume exercises the `looper resume` command via
// the built binary against a loop with no run history: it should print
// "nothing to resume" and exit 0.
func TestResumeCLI_NothingToResume(t *testing.T) {
	binPath := buildLooperBinary(t)

	repo := t.TempDir()
	loopDir := filepath.Join(repo, ".looper", "loops")
	if err := os.MkdirAll(loopDir, 0o755); err != nil {
		t.Fatal(err)
	}
	loopYAML := "name: r\nmax_iterations: 1\nsteps:\n" +
		"  - name: do\n    type: script\n    run: \"true\"\n"
	if err := os.WriteFile(filepath.Join(loopDir, "r.yaml"), []byte(loopYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "resume", "r")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("looper resume: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "nothing to resume") {
		t.Errorf("expected 'nothing to resume' in output, got %q", out)
	}
}
