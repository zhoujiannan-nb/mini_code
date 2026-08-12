package components

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/mini_code/ui/styles"
)

// Session represents a conversation session
type Session struct {
	ID    string
	Title string
}

// SessionSelectedMsg is sent when a session is selected (Enter key)
type SessionSelectedMsg struct {
	Session Session
}

// SessionDeletedMsg is sent when a session is deleted (Del key)
type SessionDeletedMsg struct {
	SessionID string
}

// SidebarModel is the session list sidebar
type SidebarModel struct {
	sessions    []Session
	selected    int
	width       int
	isRenaming  bool
	renameInput string
}

// NewSidebarModel creates a new sidebar model
func NewSidebarModel(sessions []Session) SidebarModel {
	return SidebarModel{
		sessions: sessions,
		selected: 0,
	}
}

// SetWidth sets the sidebar width
func (m *SidebarModel) SetWidth(width int) {
	m.width = width
}

// Update handles sidebar messages
func (m SidebarModel) Update(msg tea.Msg) (SidebarModel, tea.Cmd) {
	// Handle rename mode
	if m.isRenaming {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyEnter:
				// Confirm rename
				m.isRenaming = false
				if m.selected < len(m.sessions) {
					m.RenameSession(m.sessions[m.selected], m.renameInput)
				}
				m.renameInput = ""
				return m, nil
			case tea.KeyEsc:
				// Cancel rename
				m.isRenaming = false
				m.renameInput = ""
				return m, nil
			case tea.KeyBackspace:
				if len(m.renameInput) > 0 {
					m.renameInput = m.renameInput[:len(m.renameInput)-1]
				}
				return m, nil
			default:
				if msg.Type == tea.KeyRunes {
					m.renameInput += string(msg.Runes)
				}
				return m, nil
			}
		}
		return m, nil
	}

	if len(m.sessions) == 0 {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if m.selected > 0 {
				m.selected--
			}
		case tea.KeyDown:
			if m.selected < len(m.sessions)-1 {
				m.selected++
			}
		case tea.KeyEnter:
			if m.selected < len(m.sessions) {
				return m, func() tea.Msg {
					return SessionSelectedMsg{Session: m.sessions[m.selected]}
				}
			}
		case tea.KeyDelete:
			// Delete session
			if m.selected < len(m.sessions) {
				session := m.sessions[m.selected]
				m.DeleteSession(session)
				return m, func() tea.Msg {
					return SessionDeletedMsg{SessionID: session.ID}
				}
			}
		case tea.KeyF2:
			// Start rename
			if m.selected < len(m.sessions) {
				m.isRenaming = true
				m.renameInput = m.sessions[m.selected].Title
			}
		}

	case tea.MouseMsg:
		// Debug: log mouse click in sidebar
		slog.Debug("sidebar mouse click", "action", msg.Action, "button", msg.Button, "x", msg.X, "y", msg.Y)

		// Handle mouse click for session selection
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// Calculate which session was clicked
			// Layout in terminal: header (1 line) + title (1 line) + blank line (2 lines from \n\n) + session items
			// So first session is at Y=4 (0-indexed: header=0, title=1, blank=2,3, session0=4)
			clickY := msg.Y - 4 // subtract header, title, and blank lines (0-indexed session index)
			if clickY >= 0 && clickY < len(m.sessions) {
				m.selected = clickY
				return m, func() tea.Msg {
					return SessionSelectedMsg{Session: m.sessions[m.selected]}
				}
			}
		}
	}

	return m, nil
}

// View renders the sidebar
func (m SidebarModel) View() string {
	var sb strings.Builder

	// Title
	sb.WriteString(styles.SidebarTitleStyle.Render("Sessions"))
	sb.WriteString("\n\n")

	// Session list
	if len(m.sessions) == 0 {
		sb.WriteString(styles.ContentStyle.Render("  No sessions yet"))
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(styles.TextMuted).Render("  Press Ctrl+N"))
	} else {
		var style lipgloss.Style
		for i, session := range m.sessions {
			if i == m.selected {
				style = styles.SidebarItemActiveStyle
				sb.WriteString("> ")
			} else {
				style = styles.SidebarItemStyle
				sb.WriteString("  ")
			}

			// Show rename input if renaming this session
			if m.isRenaming && i == m.selected {
				sb.WriteString(styles.InputStyle.Render(m.renameInput + "_"))
			} else {
				sb.WriteString(style.Render(truncate(session.Title, m.width-6)))
			}
			sb.WriteString("\n")
		}
	}

	// Show help text
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(styles.TextMuted).Render("  Ctrl+N: New"))
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(styles.TextMuted).Render("  Del: Delete"))
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(styles.TextMuted).Render("  F2: Rename"))

	return sb.String()
}

// SetSessions updates the session list
func (m *SidebarModel) SetSessions(sessions []Session) {
	m.sessions = sessions
	if m.selected >= len(sessions) && len(sessions) > 0 {
		m.selected = len(sessions) - 1
	}
}

// AddSession adds a new session to the list
func (m *SidebarModel) AddSession(session Session) {
	m.sessions = append(m.sessions, session)
	m.selected = len(m.sessions) - 1
}

// GetSelected returns the currently selected session
func (m SidebarModel) GetSelected() *Session {
	if len(m.sessions) == 0 || m.selected >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.selected]
}

// truncate truncates a string to maxLen and adds "..." if needed
func truncate(s string, maxLen int) string {
	if maxLen < 3 {
		maxLen = 3
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// DeleteSession deletes a session from the list
func (m *SidebarModel) DeleteSession(session Session) {
	for i, s := range m.sessions {
		if s.ID == session.ID {
			m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
			break
		}
	}
	if m.selected >= len(m.sessions) && len(m.sessions) > 0 {
		m.selected = len(m.sessions) - 1
	}
}

// RenameSession renames a session in the list
func (m *SidebarModel) RenameSession(session Session, newTitle string) {
	for i, s := range m.sessions {
		if s.ID == session.ID {
			m.sessions[i].Title = newTitle
			break
		}
	}
}

// GetSelectedIndex returns the index of the currently selected session
func (m *SidebarModel) GetSelectedIndex() int {
	return m.selected
}

// FormatSessionInfo formats session info for display
func FormatSessionInfo(session Session) string {
	return fmt.Sprintf("%s %s",
		lipgloss.NewStyle().Foreground(styles.AccentPurple).Render("●"),
		lipgloss.NewStyle().Foreground(styles.TextSecondary).Render(session.Title))
}
