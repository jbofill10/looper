// Package runctx owns looper's per-iteration run directory: the KV context,
// artifacts, step logs, event log, and digest. It is written for every
// iteration regardless of what the steps do, so iteration history lives in one
// predictable place.
package runctx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RunContext is the per-iteration state store rooted at Dir.
type RunContext struct {
	Dir  string            `json:"-"`
	Vars map[string]string `json:"vars"`
}

// Event is one line in the iteration's events.jsonl.
type Event struct {
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
}

// New creates the run directory and its artifacts/ and steps/ subdirs.
func New(dir string) (*RunContext, error) {
	for _, d := range []string{dir, filepath.Join(dir, "artifacts"), filepath.Join(dir, "steps")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return &RunContext{Dir: dir, Vars: map[string]string{}}, nil
}

// Load reads a previously saved run context from dir/context.json.
func Load(dir string) (*RunContext, error) {
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		return nil, fmt.Errorf("read context.json: %w", err)
	}
	rc := &RunContext{Dir: dir, Vars: map[string]string{}}
	if err := json.Unmarshal(data, rc); err != nil {
		return nil, fmt.Errorf("parse context.json: %w", err)
	}
	if rc.Vars == nil {
		rc.Vars = map[string]string{}
	}
	return rc, nil
}

// Set stores a context variable.
func (rc *RunContext) Set(key, val string) { rc.Vars[key] = val }

// Get returns a context variable and whether it was present.
func (rc *RunContext) Get(key string) (string, bool) { v, ok := rc.Vars[key]; return v, ok }

// Env returns the context vars as a sorted slice of KEY=VALUE strings.
func (rc *RunContext) Env() []string {
	keys := make([]string, 0, len(rc.Vars))
	for k := range rc.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+rc.Vars[k])
	}
	return out
}

// ArtifactsDir is where steps drop produced files.
func (rc *RunContext) ArtifactsDir() string { return filepath.Join(rc.Dir, "artifacts") }

// StepsDir holds per-step logs and outputs files.
func (rc *RunContext) StepsDir() string { return filepath.Join(rc.Dir, "steps") }

// Save writes the KV context to dir/context.json.
func (rc *RunContext) Save() error {
	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}
	return os.WriteFile(filepath.Join(rc.Dir, "context.json"), data, 0o644)
}

// AppendEvent appends one JSON-encoded event line to dir/events.jsonl.
func (rc *RunContext) AppendEvent(ev Event) error {
	f, err := os.OpenFile(filepath.Join(rc.Dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open events log: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// WriteDigest writes the iteration rollup to dir/digest.md.
func (rc *RunContext) WriteDigest(md string) error {
	return os.WriteFile(filepath.Join(rc.Dir, "digest.md"), []byte(md), 0o644)
}
