package builder

import "github.com/charmbracelet/lipgloss"

// Palette mirrors tui's: cyan accent for structure/focus, red for errors.
var (
	colorAccent = lipgloss.Color("14")
	colorError  = lipgloss.Color("9")

	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	markerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	hintKey     = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
)

// hint renders a single "[key] label" footer entry with the key highlighted.
func hint(key, label string) string {
	return hintKey.Render("["+key+"]") + " " + label
}
