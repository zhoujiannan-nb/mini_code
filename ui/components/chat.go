package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/mini_code/ui/styles"
)

// Message represents a chat message
type Message struct {
	Role      string // "user", "assistant", "tool", "tool_call"
	Content   string
	Timestamp time.Time
	// Tool call specific fields
	ToolName   string
	ToolArgs   string
	ToolResult string
	ToolDone   bool
	Expanded   bool
}

// ToolCallStartedMsg is sent when a tool call starts
type ToolCallStartedMsg struct {
	Index int
}

// ToolCallCompletedMsg is sent when a tool call completes
type ToolCallCompletedMsg struct {
	Index int
}

// ChatModel is the chat messages display component
type ChatModel struct {
	viewport viewport.Model
	messages []Message
	width    int
	height   int
	ready    bool
}

// NewChatModel creates a new chat model
func NewChatModel() ChatModel {
	return ChatModel{
		messages: make([]Message, 0),
	}
}

// SetSize sets the viewport dimensions
func (m *ChatModel) SetSize(width, height int) {
	m.width = width
	m.height = height

	if !m.ready {
		m.viewport = viewport.New(width, height)
		m.viewport.SetContent(m.renderMessages())
		m.ready = true
	} else {
		m.viewport.Width = width
		m.viewport.Height = height
		m.viewport.SetContent(m.renderMessages())
	}
}

// AddMessage adds a new message to the chat
func (m *ChatModel) AddMessage(msg Message) {
	// Set timestamp if not provided
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	m.messages = append(m.messages, msg)
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

// ClearMessages clears all messages
func (m *ChatModel) ClearMessages() {
	m.messages = make([]Message, 0)
	m.viewport.SetContent(m.renderMessages())
}

// Update handles messages and commands
func (m ChatModel) Update(msg tea.Msg) (ChatModel, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	// Handle mouse click to set cursor position (for future text selection)
	if mouseMsg, ok := msg.(tea.MouseMsg); ok {
		if mouseMsg.Action == tea.MouseActionPress && mouseMsg.Button == tea.MouseButtonLeft {
			// Click in chat area - could be used for text selection in the future
			// For now, just pass through to viewport
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View renders the chat viewport with a scroll indicator
func (m ChatModel) View() string {
	if !m.ready {
		return "Loading..."
	}
	view := m.viewport.View()
	// Add scroll progress indicator on the right side
	scrollIndicator := m.renderScrollIndicator()
	if scrollIndicator != "" {
		return lipgloss.JoinHorizontal(lipgloss.Top, view, scrollIndicator)
	}
	return view
}

// renderScrollIndicator renders a vertical scroll progress bar
func (m ChatModel) renderScrollIndicator() string {
	if m.viewport.TotalLineCount() <= m.height {
		return ""
	}
	pct := m.viewport.ScrollPercent()
	totalH := m.height
	if totalH < 3 {
		totalH = 3
	}

	// Build a scrollbar: top cap, middle fill, bottom cap
	scrollbar := make([]string, totalH)
	for i := 0; i < totalH; i++ {
		scrollbar[i] = " "
	}

	// Calculate thumb position (1 char tall minimum)
	thumbPos := int(pct * float64(totalH-1))
	if thumbPos >= totalH {
		thumbPos = totalH - 1
	}
	scrollbar[thumbPos] = "█"

	// Style the scrollbar
	bar := strings.Join(scrollbar, "\n")
	return styles.ScrollBarStyle.Render(bar)
}

// renderMessages renders all messages to a string
func (m *ChatModel) renderMessages() string {
	if len(m.messages) == 0 {
		return styles.ContentStyle.Render("\n  Welcome to Mini Code Agent Terminal\n  Press Ctrl+N to start a new session")
	}

	var sb strings.Builder
	for i, msg := range m.messages {
		switch msg.Role {
		case "user":
			// User message: align right (like in chat apps)
			m.renderRightAligned(&sb, msg)
		case "assistant":
			// Agent message: align left
			m.renderLeftAligned(&sb, msg)
		case "error":
			// Error message: left aligned with red
			m.renderLeftAligned(&sb, msg)
		case "tool_call":
			// Tool call message with animation
			m.renderToolCall(&sb, msg, i)
		case "tool":
			// Tool result message — show with tool style, no harsh truncation
			roleStyle := styles.FormatRole(msg.Role)
			sb.WriteString(roleStyle)
			sb.WriteString("\n")
			content := msg.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			sb.WriteString(styles.ContentStyle.Render(content))
		default:
			// Other messages — truncate content to 100 chars
			roleStyle := styles.FormatRole(msg.Role)
			sb.WriteString(roleStyle)
			sb.WriteString("\n")
			content := msg.Content
			if len(content) > 100 {
				content = content[:100] + "..."
			}
			sb.WriteString(styles.ContentStyle.Render(content))
		}

		// Simple line break between messages (no divider)
		if i < len(m.messages)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// renderToolCall renders a tool call message
func (m *ChatModel) renderToolCall(sb *strings.Builder, msg Message, index int) {
	// Icon: spinning when in progress, checkmark when done
	icon := "⏳"
	if msg.ToolDone {
		icon = "✔"
	}

	// Status text
	statusText := fmt.Sprintf("%s 调用 %s 工具", icon, msg.ToolName)
	if msg.ToolDone {
		statusText += " [✓ 完成]"
	} else {
		statusText += " [执行中...]"
	}

	// Render status with appropriate style
	if msg.ToolDone {
		sb.WriteString(styles.ToolCallDoneStyle.Render(statusText))
	} else {
		sb.WriteString(styles.ToolCallPendingStyle.Render(statusText))
	}

	// Expand/collapse indicator
	if msg.ToolDone && msg.ToolResult != "" {
		if msg.Expanded {
			sb.WriteString(" ▼")
		} else {
			sb.WriteString(" ▶")
		}
	}
	sb.WriteString("\n")

	// Show expanded content
	if msg.Expanded && msg.ToolDone && msg.ToolResult != "" {
		content := msg.ToolResult
		if len(content) > 50 {
			content = content[:50] + "..."
		}
		sb.WriteString(styles.ToolCallContentStyle.Render("  " + content))
		sb.WriteString("\n")
	}
}

// renderLeftAligned renders a message aligned to the left
func (m *ChatModel) renderLeftAligned(sb *strings.Builder, msg Message) {
	// Role label (for agent) with timestamp
	timestamp := msg.Timestamp.Format("15:04")
	roleLabel := styles.AgentLabelStyle.Render("Agent")
	sb.WriteString(roleLabel)
	sb.WriteString(" ")
	sb.WriteString(styles.ContentStyle.Render(timestamp))
	sb.WriteString("\n")

	// Content with left padding
	lines := strings.Split(msg.Content, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString("  ")
		sb.WriteString(styles.AgentContentStyle.Render(truncateLine(line, m.width-8)))
		sb.WriteString("\n")
	}
}

// renderRightAligned renders a message aligned to the right
func (m *ChatModel) renderRightAligned(sb *strings.Builder, msg Message) {
	// Role label (right aligned, for user) with timestamp
	timestamp := msg.Timestamp.Format("15:04")
	roleLabel := styles.UserLabelStyle.Render("You")
	labelWithTime := fmt.Sprintf("%s %s", roleLabel, styles.ContentStyle.Render(timestamp))
	sb.WriteString(rightAlign(labelWithTime, m.width))
	sb.WriteString("\n")

	// Content with right alignment
	lines := strings.Split(msg.Content, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			sb.WriteString("\n")
			continue
		}
		content := styles.UserContentStyle.Render(truncateLine(line, m.width-8))
		sb.WriteString(rightAlign(content, m.width))
		sb.WriteString("\n")
	}
}

// ansiVisibleWidth calculates the visible display width of a string,
// properly skipping ANSI escape sequences.
func ansiVisibleWidth(s string) int {
	width := 0
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			// Skip all characters until we find a letter (end of CSI sequence)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		if isCJK(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

// rightAlign pads a string to the right side of the width
func rightAlign(s string, width int) string {
	visibleWidth := ansiVisibleWidth(s)
	padding := width - visibleWidth
	if padding <= 0 {
		return s
	}
	return strings.Repeat(" ", padding) + s
}

// truncateLine truncates a line to fit within width
func truncateLine(line string, maxWidth int) string {
	if maxWidth <= 0 {
		return line
	}

	// Calculate display width (CJK characters count as 2)
	displayWidth := 0
	var sb strings.Builder
	for _, r := range line {
		w := 1
		if isCJK(r) {
			w = 2
		}
		if displayWidth+w > maxWidth {
			break
		}
		sb.WriteRune(r)
		displayWidth += w
	}
	return sb.String()
}

// isCJK checks if a rune is a CJK character
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x2F800 && r <= 0x2FA1F)
}

// UpdateStreamingContent updates the last assistant message for streaming
func (m *ChatModel) UpdateStreamingContent(content string) {
	if len(m.messages) == 0 {
		m.messages = append(m.messages, Message{
			Role:      "assistant",
			Content:   content,
			Timestamp: time.Now(),
		})
	} else {
		// Update last message if it's from assistant
		lastIdx := len(m.messages) - 1
		if m.messages[lastIdx].Role == "assistant" {
			m.messages[lastIdx].Content = content
		} else {
			m.messages = append(m.messages, Message{
				Role:      "assistant",
				Content:   content,
				Timestamp: time.Now(),
			})
		}
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

// AddToolCallStarted adds a tool call message in started state
func (m *ChatModel) AddToolCallStarted(toolName string, args string) int {
	idx := len(m.messages)
	m.messages = append(m.messages, Message{
		Role:      "tool_call",
		ToolName:  toolName,
		ToolArgs:  args,
		ToolDone:  false,
		Expanded:  false,
		Timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return idx
}

// UpdateToolCallCompleted updates a tool call message to completed state
func (m *ChatModel) UpdateToolCallCompleted(index int, result string) {
	if index >= 0 && index < len(m.messages) {
		m.messages[index].ToolDone = true
		m.messages[index].ToolResult = result
		m.viewport.SetContent(m.renderMessages())
	}
}

// ToggleToolCallExpand toggles the expanded state of a tool call message
func (m *ChatModel) ToggleToolCallExpand(index int) {
	if index >= 0 && index < len(m.messages) && m.messages[index].Role == "tool_call" {
		m.messages[index].Expanded = !m.messages[index].Expanded
		m.viewport.SetContent(m.renderMessages())
	}
}

// FindToolCallAtLine finds the tool call message index at a given line position
func (m *ChatModel) FindToolCallAtLine(line int) int {
	currentLine := 0
	for i, msg := range m.messages {
		if msg.Role == "tool_call" {
			// Tool call takes 1-2 lines depending on expanded state
			if currentLine == line || (msg.Expanded && currentLine+1 == line) {
				return i
			}
			if msg.Expanded {
				currentLine += 2
			} else {
				currentLine++
			}
		} else {
			// Other messages take at least 2 lines
			currentLine += 2
		}
	}
	return -1
}

// AddToolCallMessage adds a tool call message (legacy method)
func (m *ChatModel) AddToolCallMessage(toolName string, args string) {
	content := fmt.Sprintf("Using tool: %s\n%s", toolName, args)
	m.AddMessage(Message{
		Role:      "tool",
		Content:   content,
		Timestamp: time.Now(),
	})
}

// AddErrorMessage adds an error message
func (m *ChatModel) AddErrorMessage(err error) {
	m.AddMessage(Message{
		Role:      "error",
		Content:   fmt.Sprintf("Error: %s", err.Error()),
		Timestamp: time.Now(),
	})
}
