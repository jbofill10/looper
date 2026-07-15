package runctx

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNew_CreatesDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "iter1")
	rc, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, sub := range []string{"", "artifacts", "steps"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Errorf("expected dir %q: %v", sub, err)
		}
	}
	if rc.ArtifactsDir() != filepath.Join(dir, "artifacts") {
		t.Errorf("ArtifactsDir = %q", rc.ArtifactsDir())
	}
	if rc.StepsDir() != filepath.Join(dir, "steps") {
		t.Errorf("StepsDir = %q", rc.StepsDir())
	}
}

func TestSetGetEnv(t *testing.T) {
	rc, _ := New(filepath.Join(t.TempDir(), "i"))
	rc.Set("TASK_ID", "42")
	rc.Set("BRANCH", "feat/x")
	if v, ok := rc.Get("TASK_ID"); !ok || v != "42" {
		t.Errorf("Get(TASK_ID) = %q,%v", v, ok)
	}
	if _, ok := rc.Get("MISSING"); ok {
		t.Errorf("Get(MISSING) should be false")
	}
	want := []string{"BRANCH=feat/x", "TASK_ID=42"} // sorted
	if got := rc.Env(); !reflect.DeepEqual(got, want) {
		t.Errorf("Env() = %v, want %v", got, want)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "i")
	rc, _ := New(dir)
	rc.Set("A", "1")
	rc.Set("B", "2")
	if err := rc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded.Vars, rc.Vars) {
		t.Errorf("loaded vars = %v, want %v", loaded.Vars, rc.Vars)
	}
	if loaded.Dir != dir {
		t.Errorf("loaded.Dir = %q, want %q", loaded.Dir, dir)
	}
}

func TestAppendEvent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "i")
	rc, _ := New(dir)
	if err := rc.AppendEvent(Event{Step: "plan", Kind: "start"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := rc.AppendEvent(Event{Step: "plan", Kind: "done", Message: "ok"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	f, _ := os.Open(filepath.Join(dir, "events.jsonl"))
	defer f.Close()
	var count int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad json line: %v", err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("event lines = %d, want 2", count)
	}
}
