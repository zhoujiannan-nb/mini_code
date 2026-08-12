package components

import (
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/mini_code/ui/styles"
)

// InputSubmitMsg is sent when the user submits input
type InputSubmitMsg struct {
	Content string
}

// pasteTimeoutMsg is sent when a paste operation times out
type pasteTimeoutMsg struct{}

// Paste burst detection constants.
//
// On Windows the console reports pasted multi-line text as a stream of
// regular key events (no Paste flag): characters arrive as KeyRunes and
// each line ending arrives as a KeyEnter (VK_RETURN). We detect these
// bursts so pasted newlines insert a line break instead of submitting.
const (
	// pasteBurstWindow is the time window for counting a burst of runes.
	pasteBurstWindow = 100 * time.Millisecond
	// pasteBurstMinRunes is the minimum rune events within the window
	// for the burst to be considered a paste.
	pasteBurstMinRunes = 3
	// pasteEnterMaxLag is the max delay between the last rune and an
	// Enter for the Enter to be treated as a pasted newline. A real
	// submit Enter is always pressed noticeably later.
	pasteEnterMaxLag = 30 * time.Millisecond
)

// InputModel is the text input component
type InputModel struct {
	textArea  textarea.Model
	width     int
	isPasting bool // true when a paste operation is in progress

	// Paste burst tracking (Windows coninput path)
	lastRuneAt time.Time // when the last rune input arrived
	runeStreak int       // rune events within the current burst window
}

// NewInputModel creates a new input model
func NewInputModel(onSubmit func(string)) InputModel {
	ta := textarea.New()
	ta.Placeholder = "Type your message... (Enter to send, Alt+Enter for newline)"
	ta.KeyMap.InsertNewline.SetKeys("alt+enter")
	ta.Focus()
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(6)
	ta.SetWidth(60)
	ta.MaxHeight = 20
	ta.Prompt = ""
	ta.FocusedStyle.Placeholder = ta.FocusedStyle.Placeholder.Foreground(styles.TextMuted)

	return InputModel{
		textArea: ta,
	}
}

// SetWidth sets the input width
func (m *InputModel) SetWidth(width int) {
	m.width = width
	m.textArea.SetWidth(width - 6) // Account for border and padding
}

// Focus gives focus to the input
func (m *InputModel) Focus() tea.Cmd {
	return m.textArea.Focus()
}

// Blur removes focus from the input
func (m *InputModel) Blur() {
	m.textArea.Blur()
}

// Update handles input messages
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	// Handle paste timeout message
	if _, ok := msg.(pasteTimeoutMsg); ok {
		m.isPasting = false
		return m, nil
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		// Track rune input for paste burst detection (see isPasteEnter)
		if keyMsg.Type == tea.KeyRunes {
			now := time.Now()
			if m.lastRuneAt.IsZero() || now.Sub(m.lastRuneAt) > pasteBurstWindow {
				m.runeStreak = 1
			} else {
				m.runeStreak++
			}
			m.lastRuneAt = now
		}

		// Handle paste events: mark paste in progress and schedule timeout
		if keyMsg.Paste {
			m.isPasting = true
			var cmd tea.Cmd
			m.textArea, cmd = m.textArea.Update(msg)
			// Schedule a timeout to clear paste mode after 100ms
			return m, tea.Batch(cmd, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
				return pasteTimeoutMsg{}
			}))
		}

		// Enter submits the message (unless in paste mode)
		if keyMsg.Type == tea.KeyEnter && !keyMsg.Alt {
			if m.isPasting || m.isPasteEnter() {
				// In paste mode, convert Enter to newline character and pass to textarea
				newlineMsg := tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune{'\n'},
				}
				var cmd tea.Cmd
				m.textArea, cmd = m.textArea.Update(newlineMsg)
				return m, cmd
			}
			value := m.textArea.Value()
			if value != "" {
				m.textArea.Reset()
				// Return a message to be handled by the main app
				return m, func() tea.Msg {
					return InputSubmitMsg{Content: value}
				}
			}
		}
	}

	// Handle mouse events: pass all mouse events to the textarea's internal viewport
	// This enables mouse wheel scrolling for pasted long text
	if _, ok := msg.(tea.MouseMsg); ok {
		var cmd tea.Cmd
		m.textArea, cmd = m.textArea.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.textArea, cmd = m.textArea.Update(msg)
	return m, cmd
}

// isPasteEnter reports whether a KeyEnter event is the newline of pasted
// multi-line text rather than a real submit. On Windows the console delivers
// pastes as a burst of key events where line endings arrive as plain
// KeyEnter events (VK_RETURN) without the Paste flag, so they are
// indistinguishable by key type alone. A deliberate Enter press is always
// separated from the preceding character by a noticeable delay, whereas the
// newlines of a paste arrive back-to-back with the surrounding characters.
func (m InputModel) isPasteEnter() bool {
	if m.lastRuneAt.IsZero() {
		return false
	}
	elapsed := time.Since(m.lastRuneAt)
	// A dense rune stream: paste burst in progress.
	if m.runeStreak >= pasteBurstMinRunes && elapsed <= pasteBurstWindow {
		return true
	}
	// A sparse but immediate Enter: short pastes like "a\nb" have a rune
	// streak of 1, yet the newline still follows within a few milliseconds.
	return elapsed <= pasteEnterMaxLag
}

// View renders the input area
func (m InputModel) View() string {
	return styles.InputStyle.Render(m.textArea.View())
}

// SetValue sets the input value
func (m *InputModel) SetValue(value string) {
	m.textArea.SetValue(value)
}

// Value returns the current input value
func (m InputModel) Value() string {
	return m.textArea.Value()
}

// SetHeight sets the input height
func (m *InputModel) SetHeight(h int) {
	m.textArea.SetHeight(h)
}

// CreateInputBar creates a styled input bar with label
func CreateInputBar(m InputModel) string {
	return lipgloss.JoinHorizontal(lipgloss.Center, m.View())
}
