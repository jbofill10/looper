// Package style centralizes the color palette and reusable lipgloss styles
// shared by the builder and tui packages, so the whole application reads as
// one consistently themed interface rather than each screen inventing its
// own look.
package style

import "github.com/charmbracelet/lipgloss"

// Palette colors. Each adapts to the terminal's light/dark background.
var (
	Accent = lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#7DD3FC"}
	Muted  = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	Good   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	Warn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	Bad    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
)

// Text styles, reused across builder and tui views.
var (
	// Title marks a screen's header line.
	Title = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	// TitleAlert marks a screen's header line when it needs attention.
	TitleAlert = lipgloss.NewStyle().Bold(true).Foreground(Bad)
	// Marker is the "▸ " focus/selection cursor.
	Marker = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	// Label styles a form field's "Name:" text.
	Label = lipgloss.NewStyle().Foreground(Muted)
	// Select styles a select field's "‹ option ›" value.
	Select = lipgloss.NewStyle().Foreground(Accent)
	// Action styles an actionable "[enter] Do thing" prompt when focused.
	Action = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	// ActionInactive styles an actionable "[enter] Do thing" prompt when it
	// isn't the focused field, so it reads as unavailable rather than as a
	// second live keybinding.
	ActionInactive = lipgloss.NewStyle().Foreground(Muted)
	// KeyHint styles the dim keybinding legend shown in footers.
	KeyHint = lipgloss.NewStyle().Foreground(Muted)
	// SubHeader styles a section label like "Steps so far:".
	SubHeader = lipgloss.NewStyle().Bold(true)
	// Error styles a validation or failure message.
	Error = lipgloss.NewStyle().Bold(true).Foreground(Bad)
	// Success styles a confirmation message.
	Success = lipgloss.NewStyle().Foreground(Good)
	// Busy styles an in-progress status message (e.g. a running draft session).
	Busy = lipgloss.NewStyle().Foreground(Warn)
)

// Glyph styles for the single-character worker status indicators.
var (
	GlyphDone             = lipgloss.NewStyle().Foreground(Good)
	GlyphRunning          = lipgloss.NewStyle().Foreground(Warn)
	GlyphNeedsYou         = lipgloss.NewStyle().Bold(true).Foreground(Bad)
	GlyphAwaitingApproval = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	GlyphEmpty            = lipgloss.NewStyle().Foreground(Muted)
)
