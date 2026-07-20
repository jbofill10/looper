// Package config defines looper's loop schema and validation.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// StepType identifies how a step is executed.
type StepType string

const (
	StepScript      StepType = "script"
	StepManual      StepType = "manual"
	StepInteractive StepType = "interactive"
	StepHeadless    StepType = "headless"
)

// OnFail is the policy applied when a script/headless step fails.
type OnFail string

const (
	OnFailAsk   OnFail = "ask"
	OnFailRetry OnFail = "retry"
	OnFailAbort OnFail = "abort"
)

// Step is one unit of work in a loop.
type Step struct {
	Name          string   `yaml:"name"`
	Type          StepType `yaml:"type"`
	Run           string   `yaml:"run,omitempty"`    // script
	Prompt        string   `yaml:"prompt,omitempty"` // interactive/headless
	Harness       string   `yaml:"harness,omitempty"`
	Outputs       []string `yaml:"outputs,omitempty"`
	SignalsNoWork bool     `yaml:"signals_no_work,omitempty"`
	OnFail        OnFail   `yaml:"on_fail,omitempty"`
}

// Loop is an ordered list of steps run as a repeating workflow.
type Loop struct {
	Name           string    `yaml:"name"`
	Concurrency    int       `yaml:"concurrency,omitempty"`
	MaxConcurrency int       `yaml:"max_concurrency,omitempty"`
	MaxIterations  int       `yaml:"max_iterations,omitempty"`
	Workspace      string    `yaml:"workspace,omitempty"` // shared|worktree
	TaskVar        string    `yaml:"task_var,omitempty"`  // the output var identifying a work unit; defaults to TASK_ID
	Schedule       *Schedule `yaml:"schedule,omitempty"`  // optional repeating trigger (see schedule.go)
	Steps          []Step    `yaml:"steps"`
}

// LoadLoop reads, parses, and validates a loop definition file.
func LoadLoop(path string) (*Loop, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read loop file: %w", err)
	}
	var l Loop
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse loop file: %w", err)
	}
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("invalid loop %q: %w", path, err)
	}
	return &l, nil
}

// LoadLoopLenient reads and YAML-parses the loop file at path without
// requiring it to pass Validate (which rejects a whole file over a single
// invalid step, or zero steps). Per-step/whole-loop validity is instead a
// caller concern (e.g. the builder's per-step error surfacing, or the
// Loops catalog showing a mid-edit loop rather than hiding it). Returns an
// error wrapping os.ErrNotExist if the file doesn't exist.
func LoadLoopLenient(path string) (*Loop, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read loop file %q: %w", path, err)
	}
	var l Loop
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse loop file %q: %w", path, err)
	}
	return &l, nil
}

// SaveLoop validates l, then marshals it to YAML and writes it to path,
// creating any missing parent directories. It re-validates before writing
// so it never produces a loop file that fails config.Loop.Validate(); on a
// validation error nothing is written.
//
// Note: gopkg.in/yaml.v3 renders a block scalar for a multi-line string,
// but a leading newline in that string becomes a literal blank first line
// when re-parsed inconsistently across yaml.v3 versions. Callers must not
// prefix multi-line Run/Prompt values with a leading newline.
func SaveLoop(l *Loop, path string) error {
	if err := l.Validate(); err != nil {
		return fmt.Errorf("invalid loop: %w", err)
	}
	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("marshal loop: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create loop directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write loop file: %w", err)
	}
	return nil
}

func knownType(t StepType) bool {
	switch t {
	case StepScript, StepManual, StepInteractive, StepHeadless:
		return true
	}
	return false
}

// Validate checks s in isolation: name set, known type, required
// type-specific fields, and a known on_fail value, defaulting OnFail to
// OnFailAsk when blank. It does not check cross-step concerns like
// duplicate names within a loop — see Loop.Validate for that.
func (s *Step) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !knownType(s.Type) {
		return fmt.Errorf("unknown type %q", s.Type)
	}
	if s.Type == StepScript && s.Run == "" {
		return fmt.Errorf("script step requires 'run'")
	}
	if (s.Type == StepInteractive || s.Type == StepHeadless) && s.Prompt == "" {
		return fmt.Errorf("%s step requires 'prompt'", s.Type)
	}
	switch s.OnFail {
	case "", OnFailAsk, OnFailRetry, OnFailAbort:
	default:
		return fmt.Errorf("invalid on_fail %q", s.OnFail)
	}
	if s.OnFail == "" {
		s.OnFail = OnFailAsk
	}
	return nil
}

// Validate checks the loop for structural errors and fills in defaults.
func (l *Loop) Validate() error {
	if l.Name == "" {
		return fmt.Errorf("loop name is required")
	}
	if len(l.Steps) == 0 {
		return fmt.Errorf("loop must have at least one step")
	}
	if l.Concurrency == 0 {
		l.Concurrency = 1
	}
	if l.MaxConcurrency == 0 {
		l.MaxConcurrency = l.Concurrency
	}
	if l.TaskVar == "" {
		l.TaskVar = "TASK_ID"
	}
	if l.Schedule != nil {
		if _, err := l.Schedule.CronSpecs(); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}
	}
	seen := map[string]bool{}
	for i := range l.Steps {
		s := &l.Steps[i]
		if err := s.Validate(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate step name %q", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}
