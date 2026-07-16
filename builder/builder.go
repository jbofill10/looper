// Package builder implements a guided, single-page Bubble Tea form for
// creating or editing a looper loop definition. Model is pure: it never
// performs file or network I/O itself. Enum-like fields (step type,
// harness, on_fail) are selects cycled with left/right rather than typed;
// free-text fields are edited directly in Update on tea.KeyMsg (no bubbles
// dependency), which keeps the whole form fully unit-testable by feeding
// synthetic key messages. The one side-effecting capability the form
// exposes — launching an interactive harness session to draft a script
// step's contents — is invoked via an injected Options.DraftFn, mirroring
// how tui.Model injects RespondFn/AttachFn to stay pure itself.
package builder

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/style"
)

// fieldID identifies one field or action on the single-page form.
type fieldID int

const (
	fName fieldID = iota
	fConcurrency
	fStepName
	fStepType
	fStepRun
	fDraft
	fStepPrompt
	fStepHarness
	fStepOutputs
	fStepOnFail
	fAddStep
	fFinish
)

// stepTypeOptions and onFailOptions define the select fields' cycling
// order; their string labels are used both for display and as the typed
// value once selected.
var (
	stepTypeOptions = []config.StepType{config.StepScript, config.StepHeadless, config.StepInteractive, config.StepManual}
	onFailOptions   = []config.OnFail{config.OnFailAsk, config.OnFailRetry, config.OnFailAbort}
)

// DraftRequest carries the preliminary context available at the moment the
// user asks to draft a script step's contents with a live harness session:
// the loop's name, the step being drafted, and the steps already added.
type DraftRequest struct {
	LoopName   string
	StepName   string
	PriorSteps []config.Step
}

// DraftedMsg reports the outcome of a draft session requested via
// Options.DraftFn: on success Content holds the drafted script text; on
// failure Err is set instead.
type DraftedMsg struct {
	Content string
	Err     error
}

// Options configures the builder's one side-effecting hook.
type Options struct {
	// DraftFn, if set, is invoked when the user requests an interactive
	// harness session to draft a script step's contents (ctrl+d while a
	// script step is being edited). It returns a tea.Cmd that performs the
	// actual session and yields a DraftedMsg.
	DraftFn func(DraftRequest) tea.Cmd
}

// Model is the Bubble Tea model driving the guided loop builder's
// single-page form.
type Model struct {
	opts Options
	pre  *config.Loop

	name        string
	concurrency string // raw text buffer; parsed on finish

	steps []config.Step

	// curX buffers hold the step currently being edited, added to steps by
	// the fAddStep action.
	curName       string
	curTypeIdx    int
	curRun        string
	curPrompt     string
	curHarnessIdx int
	curOutputs    string
	curOnFailIdx  int

	harnessOptions []string // harnessOptions[0] is always "(default)"

	focus    fieldID
	done     bool
	drafting bool
	errMsg   string

	concurrencyN int
}

// New returns a Model ready to guide the user through building a loop on a
// single page. harnessNames lists the configured harnesses (for the
// per-step harness select field; sorted for a stable order); it need not
// include a blank/default entry, New adds one. If existing is non-nil, the
// form is pre-populated from it, so editing a loop preserves its existing
// fields unless the user changes them.
func New(existing *config.Loop, harnessNames []string, opts Options) Model {
	names := append([]string{}, harnessNames...)
	sort.Strings(names)
	harnessOptions := append([]string{"(default)"}, names...)

	m := Model{
		opts:           opts,
		focus:          fName,
		harnessOptions: harnessOptions,
	}
	if existing != nil {
		m.pre = existing
		m.name = existing.Name
		if existing.Concurrency > 0 {
			m.concurrency = strconv.Itoa(existing.Concurrency)
		}
		m.steps = append([]config.Step{}, existing.Steps...)
	}
	return m
}

// Init implements tea.Model. The builder has no initial command.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case DraftedMsg:
		m.drafting = false
		if msg.Err != nil {
			m.errMsg = fmt.Sprintf("draft session failed: %v", msg.Err)
		} else {
			m.curRun = strings.TrimRight(msg.Content, "\n")
			m.errMsg = ""
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey implements the single-page form's keyboard handling: tab/down
// and shift+tab/up move focus among the currently visible fields, left and
// right cycle a focused select field's value, enter confirms/advances (or
// triggers an action field), backspace and printable runes edit a focused
// text field, and ctrl+d requests a draft session for a script step's Run
// field regardless of current focus.
func (m Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "tab", "down":
		m.focus = m.adjacentFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.focus = m.adjacentFocus(-1)
		return m, nil
	case "left":
		m.cycleSelect(m.focus, -1)
		return m, nil
	case "right":
		m.cycleSelect(m.focus, 1)
		return m, nil
	case "ctrl+d":
		return m.requestDraft()
	case "enter":
		return m.commitFocused()
	case "backspace":
		if p := m.textPtr(m.focus); p != nil {
			if r := []rune(*p); len(r) > 0 {
				*p = string(r[:len(r)-1])
			}
		}
		return m, nil
	}

	if key.Type == tea.KeyRunes {
		if p := m.textPtr(m.focus); p != nil {
			*p += string(key.Runes)
		}
	}
	return m, nil
}

// commitFocused handles an enter keypress on the focused field: on a
// select field it cycles forward (same as right, for discoverability); on
// fDraft it requests a draft session; on fAddStep it commits the
// in-progress step; on fFinish it finalizes the loop; on a text field it
// simply advances focus, matching tab.
func (m Model) commitFocused() (tea.Model, tea.Cmd) {
	switch m.focus {
	case fDraft:
		return m.requestDraft()
	case fAddStep:
		m.addStep()
		return m, nil
	case fFinish:
		m.finish()
		return m, nil
	case fStepType, fStepHarness, fStepOnFail:
		m.cycleSelect(m.focus, 1)
		return m, nil
	default:
		m.focus = m.adjacentFocus(1)
		return m, nil
	}
}

// requestDraft invokes Options.DraftFn for the step currently being
// edited, if it is a script step and a session isn't already running.
func (m Model) requestDraft() (tea.Model, tea.Cmd) {
	if stepTypeOptions[m.curTypeIdx] != config.StepScript || m.opts.DraftFn == nil || m.drafting {
		return m, nil
	}
	m.drafting = true
	m.errMsg = ""
	req := DraftRequest{
		LoopName:   strings.TrimSpace(m.name),
		StepName:   strings.TrimSpace(m.curName),
		PriorSteps: append([]config.Step{}, m.steps...),
	}
	return m, m.opts.DraftFn(req)
}

// visibleFields returns, in display order, the fields relevant to the step
// type currently selected in the in-progress step editor.
func (m Model) visibleFields() []fieldID {
	fields := []fieldID{fName, fConcurrency, fStepName, fStepType}
	switch stepTypeOptions[m.curTypeIdx] {
	case config.StepScript:
		fields = append(fields, fStepRun, fDraft)
	case config.StepHeadless, config.StepInteractive:
		fields = append(fields, fStepPrompt, fStepHarness)
	}
	fields = append(fields, fStepOutputs)
	switch stepTypeOptions[m.curTypeIdx] {
	case config.StepScript, config.StepHeadless:
		fields = append(fields, fStepOnFail)
	}
	return append(fields, fAddStep, fFinish)
}

// adjacentFocus returns the visible field delta positions away from the
// current focus, wrapping around the ends.
func (m Model) adjacentFocus(delta int) fieldID {
	fields := m.visibleFields()
	cur := 0
	for i, f := range fields {
		if f == m.focus {
			cur = i
			break
		}
	}
	n := len(fields)
	next := ((cur+delta)%n + n) % n
	return fields[next]
}

// textPtr returns a pointer to the focused text field's buffer, or nil if
// id is not a text field.
func (m *Model) textPtr(id fieldID) *string {
	switch id {
	case fName:
		return &m.name
	case fConcurrency:
		return &m.concurrency
	case fStepName:
		return &m.curName
	case fStepRun:
		return &m.curRun
	case fStepPrompt:
		return &m.curPrompt
	case fStepOutputs:
		return &m.curOutputs
	}
	return nil
}

// selectIdxPtr returns a pointer to the focused select field's option
// index, or nil if id is not a select field.
func (m *Model) selectIdxPtr(id fieldID) *int {
	switch id {
	case fStepType:
		return &m.curTypeIdx
	case fStepHarness:
		return &m.curHarnessIdx
	case fStepOnFail:
		return &m.curOnFailIdx
	}
	return nil
}

// selectOptions returns the display labels for a select field.
func (m Model) selectOptions(id fieldID) []string {
	switch id {
	case fStepType:
		return stepTypeLabels()
	case fStepHarness:
		return m.harnessOptions
	case fStepOnFail:
		return onFailLabels()
	}
	return nil
}

// cycleSelect advances id's option index by delta, wrapping around. It is
// a no-op if id is not a select field.
func (m *Model) cycleSelect(id fieldID, delta int) {
	idx := m.selectIdxPtr(id)
	if idx == nil {
		return
	}
	n := len(m.selectOptions(id))
	*idx = ((*idx+delta)%n + n) % n
}

// addStep validates and appends the in-progress step to m.steps, then
// clears the step editor buffers. On a validation failure it sets errMsg
// and moves focus to the offending field, leaving the buffers untouched.
func (m *Model) addStep() {
	name := strings.TrimSpace(m.curName)
	if name == "" {
		m.errMsg = "step name is required"
		m.focus = fStepName
		return
	}
	for _, s := range m.steps {
		if s.Name == name {
			m.errMsg = fmt.Sprintf("duplicate step name %q", name)
			m.focus = fStepName
			return
		}
	}

	t := stepTypeOptions[m.curTypeIdx]
	step := config.Step{Name: name, Type: t}

	switch t {
	case config.StepScript:
		run := strings.TrimSpace(m.curRun)
		if run == "" {
			m.errMsg = "script step requires a run command"
			m.focus = fStepRun
			return
		}
		step.Run = run
		step.OnFail = onFailOptions[m.curOnFailIdx]
	case config.StepHeadless, config.StepInteractive:
		prompt := strings.TrimSpace(m.curPrompt)
		if prompt == "" {
			m.errMsg = "prompt is required"
			m.focus = fStepPrompt
			return
		}
		step.Prompt = prompt
		if m.curHarnessIdx > 0 {
			step.Harness = m.harnessOptions[m.curHarnessIdx]
		}
		if t == config.StepHeadless {
			step.OnFail = onFailOptions[m.curOnFailIdx]
		}
	}
	step.Outputs = splitOutputs(m.curOutputs)

	m.steps = append(m.steps, step)
	m.curName, m.curRun, m.curPrompt, m.curOutputs = "", "", "", ""
	m.curTypeIdx, m.curHarnessIdx, m.curOnFailIdx = 0, 0, 0
	m.errMsg = ""
	m.focus = fStepName
}

// finish validates the loop as a whole (name set, at least one step,
// concurrency a positive integer if given) and, on success, marks the
// form Done so Loop() can assemble the result.
func (m *Model) finish() {
	if strings.TrimSpace(m.name) == "" {
		m.errMsg = "loop name is required"
		m.focus = fName
		return
	}
	if len(m.steps) == 0 {
		m.errMsg = "add at least one step before finishing"
		m.focus = fStepName
		return
	}
	n := 1
	if c := strings.TrimSpace(m.concurrency); c != "" {
		parsed, err := strconv.Atoi(c)
		if err != nil || parsed < 1 {
			m.errMsg = "concurrency must be a positive integer"
			m.focus = fConcurrency
			return
		}
		n = parsed
	}
	m.concurrencyN = n
	m.errMsg = ""
	m.done = true
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

// Loop assembles the loop built so far and reports whether the form has
// been finished. Before that, ok is false and the loop is nil.
func (m Model) Loop() (*config.Loop, bool) {
	if !m.done {
		return nil, false
	}
	return &config.Loop{
		Name:        strings.TrimSpace(m.name),
		Concurrency: m.concurrencyN,
		Steps:       m.steps,
	}, true
}

// Done reports whether the builder has finished its form.
func (m Model) Done() bool {
	return m.done
}

// stepTypeLabels returns config.StepType's string values in
// stepTypeOptions order.
func stepTypeLabels() []string {
	labels := make([]string, len(stepTypeOptions))
	for i, t := range stepTypeOptions {
		labels[i] = string(t)
	}
	return labels
}

// onFailLabels returns config.OnFail's string values in onFailOptions
// order.
func onFailLabels() []string {
	labels := make([]string, len(onFailOptions))
	for i, f := range onFailOptions {
		labels[i] = string(f)
	}
	return labels
}

// View implements tea.Model, rendering the entire form on one page: loop
// fields, the steps added so far, the in-progress step editor (fields
// relevant to its selected type only), and any validation error.
func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render("Loop builder"))

	b.WriteString(m.renderField(fName, "Loop name", m.name))
	b.WriteString(m.renderField(fConcurrency, "Concurrency (blank = 1)", m.concurrency))

	if len(m.steps) > 0 {
		fmt.Fprintf(&b, "\n%s\n", style.SubHeader.Render("Steps so far:"))
		for i, s := range m.steps {
			fmt.Fprintf(&b, "  %d. %s (%s) %s\n", i+1, s.Name, s.Type, stepSummary(s))
		}
	}

	fmt.Fprintf(&b, "\n%s\n", style.SubHeader.Render("New step:"))
	b.WriteString(m.renderField(fStepName, "Step name", m.curName))
	b.WriteString(m.renderSelect(fStepType, "Step type"))

	switch stepTypeOptions[m.curTypeIdx] {
	case config.StepScript:
		b.WriteString(m.renderField(fStepRun, "Run command", m.curRun))
		b.WriteString(m.renderDraft())
	case config.StepHeadless, config.StepInteractive:
		b.WriteString(m.renderField(fStepPrompt, "Prompt", m.curPrompt))
		b.WriteString(m.renderSelect(fStepHarness, "Harness"))
	}
	b.WriteString(m.renderField(fStepOutputs, "Outputs (comma-separated)", m.curOutputs))
	switch stepTypeOptions[m.curTypeIdx] {
	case config.StepScript, config.StepHeadless:
		b.WriteString(m.renderSelect(fStepOnFail, "On fail"))
	}

	b.WriteString(m.renderAction(fAddStep, "Add step"))
	b.WriteString(m.renderAction(fFinish, "Finish & save loop"))

	if m.errMsg != "" {
		fmt.Fprintf(&b, "\n%s\n", style.Error.Render("! "+m.errMsg))
	}
	b.WriteString("\n" + style.KeyHint.Render("[tab/shift+tab] move  [←/→] change option  [enter] confirm/select  [ctrl+d] draft script") + "\n")
	return b.String()
}

// marker returns the focus indicator for id.
func (m Model) marker(id fieldID) string {
	if m.focus == id {
		return style.Marker.Render("▸ ")
	}
	return "  "
}

func (m Model) renderField(id fieldID, label, value string) string {
	return fmt.Sprintf("%s%s %s\n", m.marker(id), style.Label.Render(label+":"), value)
}

func (m Model) renderSelect(id fieldID, label string) string {
	idx := m.selectIdx(id)
	opts := m.selectOptions(id)
	return fmt.Sprintf("%s%s %s\n", m.marker(id), style.Label.Render(label+":"), style.Select.Render("‹ "+opts[idx]+" ›"))
}

// selectIdx returns the focused select field's current option index.
func (m Model) selectIdx(id fieldID) int {
	switch id {
	case fStepType:
		return m.curTypeIdx
	case fStepHarness:
		return m.curHarnessIdx
	case fStepOnFail:
		return m.curOnFailIdx
	}
	return 0
}

func (m Model) renderAction(id fieldID, label string) string {
	return fmt.Sprintf("%s%s\n", m.marker(id), style.Action.Render("[enter] "+label))
}

func (m Model) renderDraft() string {
	if m.drafting {
		return fmt.Sprintf("%s%s\n", m.marker(fDraft), style.Busy.Render("drafting… (session running)"))
	}
	label := "[ctrl+d] Draft script with harness (opens interactive session)"
	return fmt.Sprintf("%s%s\n", m.marker(fDraft), style.Action.Render(label))
}

// stepSummary renders a short, type-appropriate summary of an already
// added step for the "Steps so far" list.
func stepSummary(s config.Step) string {
	switch s.Type {
	case config.StepScript:
		return "run: " + truncate(s.Run)
	case config.StepHeadless, config.StepInteractive:
		h := s.Harness
		if h == "" {
			h = "default"
		}
		return fmt.Sprintf("harness: %s, prompt: %s", h, truncate(s.Prompt))
	default:
		return ""
	}
}

// truncate shortens s to a display-friendly length.
func truncate(s string) string {
	const max = 40
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}
