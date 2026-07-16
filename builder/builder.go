// Package builder implements a guided, interactive Bubble Tea state
// machine for creating or editing a looper loop definition. Model is pure:
// it never performs file or network I/O itself. Text entry is handled
// directly in Update on tea.KeyMsg (no bubbles dependency), which keeps it
// fully unit-testable by feeding synthetic key messages.
package builder

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/config"
)

// stage identifies the current step of the guided builder flow.
type stage int

const (
	stageLoopName stage = iota
	stageConcurrency
	stageStepName
	stageStepType
	stageStepRun
	stageStepPrompt
	stageStepHarness
	stageStepOutputs
	stageStepOnFail
	stageAddAnother
	stageDone
)

// Model is the Bubble Tea model driving the guided loop builder. It
// accumulates loop fields as the user answers a sequence of prompts, then
// assembles a *config.Loop once the flow reaches stageDone.
type Model struct {
	stage stage
	input string

	name        string
	concurrency int
	steps       []config.Step
	cur         config.Step

	pre *config.Loop
}

// New returns a Model ready to guide the user through building a loop. If
// existing is non-nil, the builder is pre-populated from it (its name,
// concurrency, and steps carry over), so editing a loop preserves its
// existing fields unless the user changes them.
func New(existing *config.Loop) Model {
	m := Model{stage: stageLoopName}
	if existing != nil {
		m.pre = existing
		m.input = existing.Name
		m.steps = append([]config.Step{}, existing.Steps...)
	}
	return m
}

// Init implements tea.Model. The builder has no initial command.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model. Printable runes append to the current
// field's input buffer, backspace deletes the last rune, and enter commits
// the field and advances the stage.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.Type {
	case tea.KeyRunes:
		m.input += string(key.Runes)
	case tea.KeyBackspace:
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case tea.KeyEnter:
		return m.commit()
	}
	return m, nil
}

// commit handles an enter keypress: it validates and applies the current
// input buffer for the active stage, then advances to the next stage. On
// an invalid value the stage is left unchanged (a re-prompt) and the input
// buffer is cleared.
func (m Model) commit() (tea.Model, tea.Cmd) {
	val := strings.TrimSpace(m.input)

	switch m.stage {
	case stageLoopName:
		if val == "" {
			return m, nil
		}
		m.name = val
		m.input = m.concurrencyDefault()
		m.stage = stageConcurrency

	case stageConcurrency:
		n := 1
		if val != "" {
			parsed, err := strconv.Atoi(val)
			if err != nil || parsed < 1 {
				m.input = ""
				return m, nil
			}
			n = parsed
		}
		m.concurrency = n
		m.input = ""
		m.stage = stageStepName

	case stageStepName:
		if val == "" {
			return m, nil
		}
		m.cur = config.Step{Name: val}
		m.input = ""
		m.stage = stageStepType

	case stageStepType:
		t := config.StepType(strings.ToLower(val))
		m.input = ""
		switch t {
		case config.StepScript:
			m.cur.Type = t
			m.stage = stageStepRun
		case config.StepHeadless, config.StepInteractive:
			m.cur.Type = t
			m.stage = stageStepPrompt
		case config.StepManual:
			m.cur.Type = t
			m.stage = stageStepOutputs
		default:
			return m, nil
		}

	case stageStepRun:
		if val == "" {
			return m, nil
		}
		m.cur.Run = val
		m.input = ""
		m.stage = stageStepOutputs

	case stageStepPrompt:
		if val == "" {
			return m, nil
		}
		m.cur.Prompt = val
		m.input = ""
		m.stage = stageStepHarness

	case stageStepHarness:
		m.cur.Harness = val // blank ⇒ default, applied downstream
		m.input = ""
		m.stage = stageStepOutputs

	case stageStepOutputs:
		m.cur.Outputs = splitOutputs(val)
		m.input = ""
		if m.cur.Type == config.StepScript || m.cur.Type == config.StepHeadless {
			m.stage = stageStepOnFail
		} else {
			m.steps = append(m.steps, m.cur)
			m.cur = config.Step{}
			m.stage = stageAddAnother
		}

	case stageStepOnFail:
		of := config.OnFail(strings.ToLower(val))
		switch of {
		case "":
			m.cur.OnFail = config.OnFailAsk
		case config.OnFailAsk, config.OnFailRetry, config.OnFailAbort:
			m.cur.OnFail = of
		default:
			m.input = ""
			return m, nil
		}
		m.steps = append(m.steps, m.cur)
		m.cur = config.Step{}
		m.input = ""
		m.stage = stageAddAnother

	case stageAddAnother:
		switch strings.ToLower(val) {
		case "y", "yes":
			m.input = ""
			m.stage = stageStepName
		case "n", "no", "":
			m.input = ""
			m.stage = stageDone
			return m, tea.Quit
		default:
			m.input = ""
			return m, nil
		}

	case stageDone:
		return m, tea.Quit
	}

	return m, nil
}

// concurrencyDefault returns the input buffer to pre-fill when entering
// stageConcurrency: the pre-existing loop's concurrency when editing, or
// empty otherwise.
func (m Model) concurrencyDefault() string {
	if m.pre != nil && m.pre.Concurrency > 0 {
		return strconv.Itoa(m.pre.Concurrency)
	}
	return ""
}

// splitOutputs parses a comma-separated outputs field into a trimmed,
// non-empty slice of variable names.
func splitOutputs(val string) []string {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	outs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			outs = append(outs, p)
		}
	}
	return outs
}

// Loop assembles the loop built so far and reports whether the builder has
// reached stageDone. Before stageDone, ok is false and the loop is nil.
func (m Model) Loop() (*config.Loop, bool) {
	if m.stage != stageDone {
		return nil, false
	}
	return &config.Loop{
		Name:        m.name,
		Concurrency: m.concurrency,
		Steps:       m.steps,
	}, true
}

// Done reports whether the builder has completed its guided flow.
func (m Model) Done() bool {
	return m.stage == stageDone
}

// View implements tea.Model, rendering the current prompt and input
// buffer.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.prompt())
	b.WriteString("\n> ")
	b.WriteString(m.input)
	b.WriteString("\n")
	return b.String()
}

// prompt returns the human-readable question for the current stage.
func (m Model) prompt() string {
	switch m.stage {
	case stageLoopName:
		return "Loop name:"
	case stageConcurrency:
		return "Concurrency (blank = 1):"
	case stageStepName:
		return "Step name:"
	case stageStepType:
		return "Step type (script|headless|interactive|manual):"
	case stageStepRun:
		return "Run command:"
	case stageStepPrompt:
		return "Prompt:"
	case stageStepHarness:
		return "Harness (blank = default):"
	case stageStepOutputs:
		return "Outputs (comma-separated, blank = none):"
	case stageStepOnFail:
		return "On fail (ask|retry|abort, blank = ask):"
	case stageAddAnother:
		return "Add another step? (y/n):"
	default:
		return "Done."
	}
}
