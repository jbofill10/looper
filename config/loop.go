// Package config defines looper's loop schema and validation.
package config

import (
	"fmt"
	"os"

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
	Name           string `yaml:"name"`
	Concurrency    int    `yaml:"concurrency,omitempty"`
	MaxConcurrency int    `yaml:"max_concurrency,omitempty"`
	MaxIterations  int    `yaml:"max_iterations,omitempty"`
	Workspace      string `yaml:"workspace,omitempty"` // shared|worktree
	TaskVar        string `yaml:"task_var,omitempty"`  // the output var identifying a work unit; defaults to TASK_ID
	Steps          []Step `yaml:"steps"`
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

func knownType(t StepType) bool {
	switch t {
	case StepScript, StepManual, StepInteractive, StepHeadless:
		return true
	}
	return false
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
	seen := map[string]bool{}
	for i := range l.Steps {
		s := &l.Steps[i]
		if s.Name == "" {
			return fmt.Errorf("step %d: name is required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate step name %q", s.Name)
		}
		seen[s.Name] = true
		if !knownType(s.Type) {
			return fmt.Errorf("step %q: unknown type %q", s.Name, s.Type)
		}
		if s.Type == StepScript && s.Run == "" {
			return fmt.Errorf("step %q: script step requires 'run'", s.Name)
		}
		if (s.Type == StepInteractive || s.Type == StepHeadless) && s.Prompt == "" {
			return fmt.Errorf("step %q: %s step requires 'prompt'", s.Name, s.Type)
		}
		switch s.OnFail {
		case "", OnFailAsk, OnFailRetry, OnFailAbort:
		default:
			return fmt.Errorf("step %q: invalid on_fail %q", s.Name, s.OnFail)
		}
		if s.OnFail == "" {
			s.OnFail = OnFailAsk
		}
	}
	return nil
}
