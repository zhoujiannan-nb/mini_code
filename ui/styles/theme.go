package styles

import "github.com/charmbracelet/lipgloss"

// Codex-inspired dark theme colors
var (
	// Background colors
	BgDark    = lipgloss.Color("#1e1e2e")
	BgSurface = lipgloss.Color("#313244")
	BgOverlay = lipgloss.Color("#45475a")

	// Text colors
	TextPrimary   = lipgloss.Color("#cdd6f4")
	TextSecondary = lipgloss.Color("#a6adc8")
	TextMuted     = lipgloss.Color("#6c7086")

	// Accent colors
	AccentBlue   = lipgloss.Color("#89b4fa")
	AccentGreen  = lipgloss.Color("#a6e3a1")
	AccentRed    = lipgloss.Color("#f38ba8")
	AccentYellow = lipgloss.Color("#f9e2af")
	AccentPurple = lipgloss.Color("#cba6f7")
)

// Styles for different UI elements
var (
	// Title bar style
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(AccentBlue).
			Background(BgDark).
			Padding(0, 1)

	// User message styles (left aligned)
	UserLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(AccentBlue).
			PaddingLeft(1)

	UserContentStyle = lipgloss.NewStyle().
				Foreground(TextPrimary).
				PaddingLeft(2)

	// Agent message styles (right aligned)
	AgentLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(AccentGreen).
			PaddingRight(1)

	AgentContentStyle = lipgloss.NewStyle().
				Foreground(TextPrimary).
				PaddingRight(2)

	// Tool/Error message styles
	ToolMsgStyle = lipgloss.NewStyle().
			Foreground(AccentYellow).
			Bold(true).
			PaddingLeft(1)

	ErrorMsgStyle = lipgloss.NewStyle().
			Foreground(AccentRed).
			Bold(true).
			PaddingLeft(1)

	// Content styles
	ContentStyle = lipgloss.NewStyle().
			Foreground(TextPrimary).
			PaddingLeft(2)

	// Sidebar styles
	SidebarTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(AccentPurple).
				Padding(0, 1)

	SidebarItemStyle = lipgloss.NewStyle().
				Foreground(TextSecondary).
				PaddingLeft(1)

	SidebarItemActiveStyle = lipgloss.NewStyle().
				Foreground(AccentBlue).
				Bold(true).
				PaddingLeft(1)

	// Input area styles
	InputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentBlue).
			Padding(0, 1)

	// Status bar styles
	StatusBarStyle = lipgloss.NewStyle().
			Foreground(TextMuted).
			Background(BgSurface).
			Padding(0, 1)

	// Divider style
	DividerStyle = lipgloss.NewStyle().
			Foreground(BgOverlay)

	// Header style
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(AccentBlue).
			Padding(0, 1).
			MarginBottom(1)

	// Log view style
	LogViewStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(BgOverlay).
			Foreground(TextMuted).
			Padding(0, 1)

	// Code block style (for future markdown rendering)
	CodeStyle = lipgloss.NewStyle().
			Foreground(AccentGreen).
			Background(BgSurface).
			Padding(0, 1)

	// Tool call styles
	ToolCallPendingStyle = lipgloss.NewStyle().
				Foreground(AccentYellow).
				Bold(true).
				PaddingLeft(1)

	ToolCallDoneStyle = lipgloss.NewStyle().
				Foreground(AccentGreen).
				Bold(true).
				PaddingLeft(1)

	ToolCallContentStyle = lipgloss.NewStyle().
				Foreground(TextSecondary).
				PaddingLeft(2)

	// Tool call argument style (muted, reads like a smaller font)
	ToolCallArgsStyle = lipgloss.NewStyle().
				Foreground(TextMuted).
				PaddingLeft(2)

	// Scroll bar indicator style
	ScrollBarStyle = lipgloss.NewStyle().
			Foreground(AccentBlue).
			PaddingLeft(0)
)

// Helper functions for formatting
func FormatRole(role string) string {
	switch role {
	case "user":
		return UserLabelStyle.Render("You")
	case "assistant":
		return AgentLabelStyle.Render("Agent")
	case "tool":
		return ToolMsgStyle.Render("Tool")
	default:
		return role
	}
}

func FormatStatus(status string) string {
	return StatusBarStyle.Render(status)
}
