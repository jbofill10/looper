// Package builder implements a file-backed Bubble Tea list view over a
// looper loop's steps. The loop's YAML file on disk is the source of
// truth at all times: Model reloads from it after every interactive
// authoring session and applies delete/reorder directly to it, rather
// than assembling a Loop in memory to save once at the end. The one
// side-effecting capability the view exposes — launching an interactive
// claude session to create or edit a step — is invoked via an injected
// Options.AuthorFn, mirroring how tui.Model injects RespondFn/AttachFn to
// stay pure itself.
package builder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/style"
)

// AuthorRequest carries the context an Options.AuthorFn needs to launch a
// step-authoring session: the project directory and loop file path to run
// it against, which step to edit (blank means create a new one), and that
// step's current validation error, if any.
type AuthorRequest struct {
	ProjectDir    string
	LoopPath      string
	StepName      string
	ValidationErr error
}

// SessionDoneMsg reports that an authoring session invoked via
// Options.AuthorFn has exited (the user detached). Err is set only if the
// session itself failed to start or run (not for the edited file failing
// validation — that's surfaced via StepErrors after the reload Update
// performs on receipt of this message).
type SessionDoneMsg struct {
	Err error
}

// Options configures the builder's one side-effecting hook.
type Options struct {
	// AuthorFn, if set, is invoked when the user requests a step-authoring
	// session (create-step or edit-step). It returns a tea.Cmd that
	// performs the actual session and yields a SessionDoneMsg.
	AuthorFn func(AuthorRequest) tea.Cmd
}

// Model is the Bubble Tea model driving the file-backed step list.
type Model struct {
	opts Options

	projectDir string
	loopPath   string

	loop       *config.Loop
	stepErrors map[string]error

	cursor    int
	authoring bool
	quit      bool
	errMsg    string

	awaitingConcurrency bool
	concurrencyChoice   int
}

// New loads the loop at loopPath, or, if it doesn't exist yet, writes a
// minimal skeleton there first (name derived from loopPath's base name,
// empty steps) before loading it. projectDir is the working directory a
// step-authoring session is launched in.
//
// Loading (both here and on SessionDoneMsg reload) intentionally does not
// go through config.LoadLoop's strict, whole-loop Validate: an authoring
// session may leave the file in a state where one step is incomplete
// (e.g. a freshly created interactive step with no prompt yet), and the
// builder's whole purpose is to keep showing that file's steps — flagging
// the bad one via StepErrors — rather than refusing to load it at all.
func New(projectDir, loopPath string, opts Options) (Model, error) {
	loop, err := config.LoadLoopLenient(loopPath)
	freshLoop := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Model{}, fmt.Errorf("load loop: %w", err)
		}
		skeleton := &config.Loop{Name: loopName(loopPath), Concurrency: 1, Steps: []config.Step{}}
		if saveErr := writeLoopFile(skeleton, loopPath); saveErr != nil {
			return Model{}, fmt.Errorf("write new loop skeleton: %w", saveErr)
		}
		loop = skeleton
		freshLoop = true
	}

	m := Model{
		opts:                opts,
		projectDir:          projectDir,
		loopPath:            loopPath,
		loop:                loop,
		awaitingConcurrency: freshLoop,
		concurrencyChoice:   loop.Concurrency,
	}
	if m.concurrencyChoice < 1 {
		m.concurrencyChoice = 1
	}
	m.revalidate()
	return m, nil
}

// writeLoopFile marshals l to YAML and writes it to path, creating any
// missing parent directories, without requiring l to pass Validate. Used
// for the initial skeleton (deliberately zero steps) and for every other
// direct, session-free mutation the builder performs (delete, reorder,
// confirming the concurrency stage) — the loop is deliberately allowed to
// be transiently invalid on disk so validation stays advisory, never
// blocking.
func writeLoopFile(l *config.Loop, path string) error {
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

// loopName derives a loop's name from its file path: the base name
// without a .yaml/.yml extension.
func loopName(loopPath string) string {
	base := loopPath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".yaml")
	base = strings.TrimSuffix(base, ".yml")
	return base
}

// Steps returns the current in-memory step list (as of the last load).
func (m Model) Steps() []config.Step {
	return m.loop.Steps
}

// StepErrors returns the current per-step validation errors, keyed by
// step name, as of the last reload.
func (m Model) StepErrors() map[string]error {
	return m.stepErrors
}

// Path returns the loop file path this model is backed by.
func (m Model) Path() string {
	return m.loopPath
}

// Quit reports whether the user has requested to leave the builder.
func (m Model) Quit() bool {
	return m.quit
}

// AwaitingConcurrency reports whether the model is still on the initial
// concurrency-selection stage shown once for a brand-new loop, before its
// step list becomes editable.
func (m Model) AwaitingConcurrency() bool {
	return m.awaitingConcurrency
}

// Concurrency returns the concurrency value currently selected (while
// AwaitingConcurrency) or already saved (once confirmed).
func (m Model) Concurrency() int {
	return m.concurrencyChoice
}

// revalidate recomputes m.stepErrors from m.loop.Steps by calling
// (*config.Step).Validate() on each, independent of the others, and also
// flagging duplicate step names — a cross-step, loop-level concern that
// Step.Validate() alone can't catch (only config.Loop.Validate() does),
// but which the builder needs to surface visually before it manifests as
// an opaque save/delete failure.
func (m *Model) revalidate() {
	counts := map[string]int{}
	for i := range m.loop.Steps {
		counts[m.loop.Steps[i].Name]++
	}

	errs := map[string]error{}
	for i := range m.loop.Steps {
		s := &m.loop.Steps[i]
		err := s.Validate()
		if counts[s.Name] > 1 {
			dupErr := fmt.Errorf("duplicate step name %q", s.Name)
			if err != nil {
				err = fmt.Errorf("%w; %v", err, dupErr)
			} else {
				err = dupErr
			}
		}
		if err != nil {
			errs[s.Name] = err
		}
	}
	m.stepErrors = errs
}

// WithCursor returns m with its cursor set to i, clamped to a valid step
// index. Used by embedders (e.g. the fleet TUI's inline step list) that
// track their own selection and need the builder's next c/e/d/reorder key
// to act on that same step.
func (m Model) WithCursor(i int) Model {
	if i < 0 {
		i = 0
	}
	if max := len(m.loop.Steps) - 1; i > max {
		i = max
	}
	if i < 0 {
		i = 0
	}
	m.cursor = i
	return m
}

// Init implements tea.Model. The builder has no initial command.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionDoneMsg:
		m.authoring = false
		if msg.Err != nil {
			m.errMsg = fmt.Sprintf("authoring session failed: %v", msg.Err)
			return m, nil
		}
		reloaded, err := config.LoadLoopLenient(m.loopPath)
		if err != nil {
			// The file may be mid-edit and momentarily invalid; keep the
			// last good in-memory copy but surface the reload error.
			m.errMsg = fmt.Sprintf("reload after session: %v", err)
			return m, nil
		}
		m.loop = reloaded
		if m.cursor >= len(m.loop.Steps) {
			m.cursor = len(m.loop.Steps) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.revalidate()
		m.errMsg = ""
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey implements the step list's keyboard handling: up/k and
// down/j move the cursor, c requests a create-step session, e requests an
// edit-step session for the selected step, d deletes the selected step
// (rewriting the file directly, no session), and q requests to quit.
func (m Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.awaitingConcurrency {
		return m.handleConcurrencyKey(key)
	}
	if m.authoring {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.loop.Steps)-1 {
			m.cursor++
		}
		return m, nil
	case "shift+up":
		return m.moveSelected(-1)
	case "shift+down":
		return m.moveSelected(1)
	case "c":
		return m.requestAuthor("")
	case "e":
		if len(m.loop.Steps) == 0 {
			return m, nil
		}
		return m.requestAuthor(m.loop.Steps[m.cursor].Name)
	case "d":
		return m.deleteSelected()
	case "q":
		m.quit = true
		return m, nil
	}
	return m, nil
}

// handleConcurrencyKey handles the initial concurrency-selection stage
// for a brand-new loop: left/right adjust the value (minimum 1), enter
// confirms and writes it to the file, q quits without confirming.
func (m Model) handleConcurrencyKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "left":
		if m.concurrencyChoice > 1 {
			m.concurrencyChoice--
		}
		return m, nil
	case "right":
		m.concurrencyChoice++
		return m, nil
	case "enter":
		updated := *m.loop
		updated.Concurrency = m.concurrencyChoice
		if err := writeLoopFile(&updated, m.loopPath); err != nil {
			m.errMsg = fmt.Sprintf("set concurrency: %v", err)
			return m, nil
		}
		m.loop = &updated
		m.awaitingConcurrency = false
		m.errMsg = ""
		return m, nil
	case "q":
		m.quit = true
		return m, nil
	}
	return m, nil
}

// requestAuthor invokes Options.AuthorFn for a create (stepName == "") or
// edit (stepName set) session, including the selected step's current
// validation error for an edit request.
func (m Model) requestAuthor(stepName string) (tea.Model, tea.Cmd) {
	if m.opts.AuthorFn == nil || m.authoring {
		return m, nil
	}
	m.authoring = true
	m.errMsg = ""
	req := AuthorRequest{
		ProjectDir:    m.projectDir,
		LoopPath:      m.loopPath,
		StepName:      stepName,
		ValidationErr: m.stepErrors[stepName],
	}
	return m, m.opts.AuthorFn(req)
}

// deleteSelected removes the selected step from the loop and saves it
// directly, with no authoring session involved.
func (m Model) deleteSelected() (tea.Model, tea.Cmd) {
	if len(m.loop.Steps) == 0 {
		return m, nil
	}
	next := append([]config.Step{}, m.loop.Steps[:m.cursor]...)
	next = append(next, m.loop.Steps[m.cursor+1:]...)
	updated := *m.loop
	updated.Steps = next
	if err := writeLoopFile(&updated, m.loopPath); err != nil {
		m.errMsg = fmt.Sprintf("delete step: %v", err)
		return m, nil
	}
	m.loop = &updated
	if m.cursor >= len(m.loop.Steps) {
		m.cursor = len(m.loop.Steps) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.revalidate()
	m.errMsg = ""
	return m, nil
}

// moveSelected swaps the selected step with its neighbor delta positions
// away (-1 = earlier, +1 = later), writes the reordered list directly to
// the file (no authoring session), and moves the cursor to follow it. A
// delta that would move past either end of the list is a no-op.
func (m Model) moveSelected(delta int) (tea.Model, tea.Cmd) {
	target := m.cursor + delta
	if target < 0 || target >= len(m.loop.Steps) {
		return m, nil
	}
	next := append([]config.Step{}, m.loop.Steps...)
	next[m.cursor], next[target] = next[target], next[m.cursor]

	updated := *m.loop
	updated.Steps = next
	if err := writeLoopFile(&updated, m.loopPath); err != nil {
		m.errMsg = fmt.Sprintf("reorder step: %v", err)
		return m, nil
	}
	m.loop = &updated
	m.cursor = target
	m.revalidate()
	m.errMsg = ""
	return m, nil
}

// View implements tea.Model, rendering the step list with the cursor and
// any invalid steps in red.
func (m Model) View() string {
	if m.awaitingConcurrency {
		return m.viewConcurrency()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("Loop: %s", m.loop.Name)))

	if len(m.loop.Steps) == 0 {
		b.WriteString(style.Label.Render("(no steps yet — press c to create one)") + "\n")
	}
	for i, s := range m.loop.Steps {
		marker := "  "
		if i == m.cursor {
			marker = style.Marker.Render("▸ ")
		}
		line := fmt.Sprintf("%s (%s)", s.Name, s.Type)
		if err, bad := m.stepErrors[s.Name]; bad {
			line = style.Error.Render(line + " — " + err.Error())
		}
		fmt.Fprintf(&b, "%s%s\n", marker, line)
	}

	if m.authoring {
		fmt.Fprintf(&b, "\n%s\n", style.Busy.Render("session running…"))
	}
	if m.errMsg != "" {
		fmt.Fprintf(&b, "\n%s\n", style.Error.Render("! "+m.errMsg))
	}
	b.WriteString("\n" + style.KeyHint.Render("[c] create-step  [e] edit-step  [d] delete  [↑/↓] cursor  [shift+↑/↓] reorder  [q] quit") + "\n")
	return b.String()
}

// viewConcurrency renders the one-time concurrency-selection stage shown
// for a brand-new loop, before its step list becomes editable.
func (m Model) viewConcurrency() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("Loop: %s", m.loop.Name)))
	fmt.Fprintf(&b, "%s %s\n", style.Label.Render("Concurrency:"), style.Select.Render(fmt.Sprintf("‹ %d ›", m.concurrencyChoice)))
	b.WriteString("\n" + style.KeyHint.Render("[←/→] change  [enter] confirm  [q] quit") + "\n")
	return b.String()
}
