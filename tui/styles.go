package tui

import "github.com/charmbracelet/lipgloss"

// Color palette for the fleet & focus TUI. Kept to a small, consistent set
// so every view reads as one system: cyan for structural/brand elements,
// green/yellow/blue/gray for the four worker states, red for errors and the
// needs-human badge.
var (
	colorAccent  = lipgloss.Color("14") // bright cyan — headers, cursor, borders
	colorSuccess = lipgloss.Color("10") // bright green — done
	colorWarn    = lipgloss.Color("11") // bright yellow — needs human
	colorWorking = lipgloss.Color("12") // bright blue — actively running
	colorMuted   = lipgloss.Color("8")  // gray — idle / no work
	colorError   = lipgloss.Color("9")  // bright red — errors, NEED YOU badge
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	badgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	rowStyle    = lipgloss.NewStyle().Foreground(colorAccent)
	dimStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	hintKey     = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	stateStyle  = lipgloss.NewStyle().Foreground(colorWorking)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorError)

	glyphWorking = lipgloss.NewStyle().Foreground(colorWorking)
	glyphWaiting = lipgloss.NewStyle().Bold(true).Foreground(colorWarn)
	glyphDone    = lipgloss.NewStyle().Foreground(colorSuccess)
	glyphIdle    = lipgloss.NewStyle().Foreground(colorMuted)
)

// hint renders a single "[key] label" footer entry with the key highlighted.
func hint(key, label string) string {
	return hintKey.Render("["+key+"]") + " " + label
}

// styledGlyph returns row's status glyph (see glyph) wrapped in the color
// matching its state: blue working, yellow needs-human, green done, gray
// idle.
func styledGlyph(row workerRow) string {
	g := glyph(row)
	switch {
	case row.PendingReqID != "":
		return glyphWaiting.Render(g)
	case row.Status == "done" || row.Status == "stopped" || row.Status == "error":
		return glyphDone.Render(g)
	case row.Step == "" && row.State == "":
		return glyphIdle.Render(g)
	default:
		return glyphWorking.Render(g)
	}
}
