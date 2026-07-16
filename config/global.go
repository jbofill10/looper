package config

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Sentinels are the marker strings a harness emits in its output to signal
// special states: needing human input, being done, or having no work.
type Sentinels struct {
	NeedsInput string `yaml:"needs_input,omitempty"`
	Done       string `yaml:"done,omitempty"`
	NoWork     string `yaml:"no_work,omitempty"`
}

// Harness describes how to invoke an agentic coding tool, both interactively
// and headlessly, plus the sentinels it uses to signal state.
type Harness struct {
	Interactive []string  `yaml:"interactive,omitempty"`
	Headless    []string  `yaml:"headless,omitempty"`
	Sentinels   Sentinels `yaml:"sentinels,omitempty"`
}

// Global is looper's user-level configuration: the default harness to use
// and the set of known harnesses.
type Global struct {
	DefaultHarness string             `yaml:"default_harness,omitempty"`
	Harnesses      map[string]Harness `yaml:"harnesses,omitempty"`
}

// DefaultGlobal returns the built-in configuration: a single "claude" harness
// used by default.
func DefaultGlobal() *Global {
	return &Global{
		DefaultHarness: "claude",
		Harnesses: map[string]Harness{
			"claude": {
				Interactive: []string{"claude"},
				Headless:    []string{"claude", "-p", "{{PROMPT}}"},
				Sentinels: Sentinels{
					NeedsInput: "@@LOOPER:NEEDS_INPUT@@",
					Done:       "@@LOOPER:DONE@@",
					NoWork:     "@@LOOPER:NO_WORK@@",
				},
			},
		},
	}
}

// LoadGlobal reads and parses the global config file at path. If the file
// does not exist, it returns the built-in defaults. Harnesses omitted by the
// user (in particular "claude") still resolve, merged in from the defaults.
func LoadGlobal(path string) (*Global, error) {
	def := DefaultGlobal()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return def, nil
		}
		return nil, fmt.Errorf("read global config: %w", err)
	}

	var g Global
	if err := yaml.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("parse global config: %w", err)
	}

	if g.Harnesses == nil {
		g.Harnesses = map[string]Harness{}
	}
	for name, h := range def.Harnesses {
		if _, ok := g.Harnesses[name]; !ok {
			g.Harnesses[name] = h
		}
	}
	if g.DefaultHarness == "" {
		g.DefaultHarness = def.DefaultHarness
	}

	return &g, nil
}

// HarnessNames returns the sorted names of g's configured harnesses.
func (g *Global) HarnessNames() []string {
	names := make([]string, 0, len(g.Harnesses))
	for name := range g.Harnesses {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolveHarness looks up a harness by name; an empty name resolves to
// g.DefaultHarness. It errors if the named harness is unknown.
func (g *Global) ResolveHarness(name string) (Harness, error) {
	if name == "" {
		name = g.DefaultHarness
	}
	h, ok := g.Harnesses[name]
	if !ok {
		return Harness{}, fmt.Errorf("unknown harness %q", name)
	}
	return h, nil
}
