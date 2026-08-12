package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/mini_code/ui/styles"
)

// LogViewModel is a scrollable log display component
type LogViewModel struct {
	viewport viewport.Model
	lines    []string
	width    int
	height   int
	ready    bool
}

// NewLogViewModel creates a new log viewport model
func NewLogViewModel() LogViewModel {
	return LogViewModel{
		lines: make([]string, 0),
	}
}

// SetSize sets the viewport dimensions
func (m *LogViewModel) SetSize(width, height int) {
	m.width = width
	m.height = height

	if !m.ready {
		m.viewport = viewport.New(width, height)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = width
		m.viewport.Height = height
		m.viewport.SetContent(m.renderContent())
	}
}

// AddLine adds a log line to the viewport
func (m *LogViewModel) AddLine(line string) {
	m.lines = append(m.lines, line)
	// Keep only last 200 lines to avoid memory issues
	if len(m.lines) > 200 {
		m.lines = m.lines[len(m.lines)-200:]
	}
	m.viewport.SetContent(m.renderContent())
	m.viewport.GotoBottom()
}

// ClearLines clears all log lines
func (m *LogViewModel) ClearLines() {
	m.lines = make([]string, 0)
	m.viewport.SetContent(m.renderContent())
}

// Update handles viewport messages (scrolling)
func (m LogViewModel) Update(msg tea.Msg) (LogViewModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View renders the log viewport with a scroll indicator
func (m LogViewModel) View() string {
	if !m.ready {
		return styles.LogViewStyle.Render(" Logs ")
	}
	view := m.viewport.View()
	scrollIndicator := m.renderScrollIndicator()
	if scrollIndicator != "" {
		return styles.LogViewStyle.Render(
			lipgloss.JoinHorizontal(lipgloss.Top, view, scrollIndicator),
		)
	}
	return styles.LogViewStyle.Render(view)
}

// renderScrollIndicator renders a vertical scroll progress bar
func (m LogViewModel) renderScrollIndicator() string {
	if m.viewport.TotalLineCount() <= m.height {
		return ""
	}
	pct := m.viewport.ScrollPercent()
	totalH := m.height
	if totalH < 3 {
		totalH = 3
	}

	// Build a scrollbar: only a thumb marker
	scrollbar := make([]string, totalH)
	for i := 0; i < totalH; i++ {
		scrollbar[i] = " "
	}

	// Calculate thumb position
	thumbPos := int(pct * float64(totalH-1))
	if thumbPos >= totalH {
		thumbPos = totalH - 1
	}
	scrollbar[thumbPos] = "█"

	bar := strings.Join(scrollbar, "\n")
	return styles.ScrollBarStyle.Render(bar)
}

// renderContent renders all log lines to a string
func (m *LogViewModel) renderContent() string {
	if len(m.lines) == 0 {
		return styles.ContentStyle.Render("  Waiting for logs...")
	}
	var sb strings.Builder
	for _, line := range m.lines {
		// Color-code by log level
		styled := m.styleLine(line)
		sb.WriteString(styled)
		sb.WriteString("\n")
	}
	return sb.String()
}

// styleLine applies color based on log level
func (m *LogViewModel) styleLine(line string) string {
	upper := strings.ToUpper(line)
	if strings.Contains(upper, "ERROR") || strings.Contains(upper, "FAIL") {
		return styles.ErrorMsgStyle.Render(line)
	}
	if strings.Contains(upper, "WARN") {
		return lipgloss.NewStyle().Foreground(styles.AccentYellow).Render(line)
	}
	if strings.Contains(upper, "INFO") {
		return lipgloss.NewStyle().Foreground(styles.TextMuted).Render(line)
	}
	return line
}
