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

// Tool call status values
const (
	ToolStatusDecided = "decided" // 已发起，等待执行
	ToolStatusRunning = "running" // 执行中
	ToolStatusSuccess = "success" // 成功
)

// Message represents a chat message
type Message struct {
	Role    string // "user", "assistant", "tool", "tool_call"
	Content string
	// ReasoningContent is the model's thinking process (rendered muted)
	ReasoningContent string
	Timestamp        time.Time
	// Tool call specific fields
	ToolName   string
	ToolArgs   string
	ToolResult string
	ToolStatus string // ToolStatusDecided / ToolStatusRunning / ToolStatusSuccess
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
	icon := "⏳"
	statusTag := "已发起"
	statusStyle := styles.ToolCallPendingStyle
	switch msg.ToolStatus {
	case ToolStatusRunning:
		statusTag = "执行中..."
	case ToolStatusSuccess:
		icon = "✔"
		statusTag = "成功"
		statusStyle = styles.ToolCallDoneStyle
	}

	statusText := fmt.Sprintf("%s 发起 %s 工具调用 [%s]", icon, msg.ToolName, statusTag)

	// Expand/collapse indicator for completed calls with results
	if msg.ToolStatus == ToolStatusSuccess && msg.ToolResult != "" {
		if msg.Expanded {
			statusText += " ▼"
		} else {
			statusText += " ▶"
		}
	}
	sb.WriteString(statusStyle.Render(statusText))
	sb.WriteString("\n")

	// Call arguments in muted style below the call line
	if msg.ToolArgs != "" {
		args := strings.ReplaceAll(msg.ToolArgs, "\n", " ")
		sb.WriteString(styles.ToolCallArgsStyle.Render(truncateLine(args, m.width-8)))
		sb.WriteString("\n")
	}

	// Expanded result (collapsed by default, first 50 chars)
	if msg.Expanded && msg.ToolStatus == ToolStatusSuccess && msg.ToolResult != "" {
		content := msg.ToolResult
		if len(content) > 50 {
			content = content[:50] + "..."
		}
		sb.WriteString(styles.ToolCallContentStyle.Render(content))
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

	// Reasoning block: muted thinking process above the reply
	if msg.ReasoningContent != "" {
		sb.WriteString(styles.ThinkingLabelStyle.Render("💭 思考过程"))
		sb.WriteString("\n")
		for _, line := range strings.Split(msg.ReasoningContent, "\n") {
			if strings.TrimSpace(line) == "" {
				sb.WriteString("\n")
				continue
			}
			sb.WriteString("  ")
			sb.WriteString(styles.ThinkingContentStyle.Render(truncateLine(line, m.width-8)))
			sb.WriteString("\n")
		}
		sb.WriteString(styles.ThinkingDividerStyle.Render("──"))
		sb.WriteString("\n")
	}

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

// UpdateStreamingContent updates the last assistant message for streaming.
// content is the accumulated text; reasoning (if non-empty) is the accumulated
// thinking process. An empty reasoning keeps the previous value so final
// non-streaming updates don't wipe it out.
func (m *ChatModel) UpdateStreamingContent(content string, reasoning string) {
	if len(m.messages) == 0 {
		m.messages = append(m.messages, Message{
			Role:             "assistant",
			Content:          content,
			ReasoningContent: reasoning,
			Timestamp:        time.Now(),
		})
	} else {
		// Update last message if it's from assistant
		lastIdx := len(m.messages) - 1
		if m.messages[lastIdx].Role == "assistant" {
			m.messages[lastIdx].Content = content
			if reasoning != "" {
				m.messages[lastIdx].ReasoningContent = reasoning
			}
		} else {
			m.messages = append(m.messages, Message{
				Role:             "assistant",
				Content:          content,
				ReasoningContent: reasoning,
				Timestamp:        time.Now(),
			})
		}
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

// AddToolCallDecided adds a tool call message in decided (pending) state
func (m *ChatModel) AddToolCallDecided(toolName string, args string) int {
	idx := len(m.messages)
	m.messages = append(m.messages, Message{
		Role:       "tool_call",
		ToolName:   toolName,
		ToolArgs:   args,
		ToolStatus: ToolStatusDecided,
		Timestamp:  time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return idx
}

// UpdateToolCallRunning updates a tool call message to running state
func (m *ChatModel) UpdateToolCallRunning(index int) {
	if index >= 0 && index < len(m.messages) {
		m.messages[index].ToolStatus = ToolStatusRunning
		m.viewport.SetContent(m.renderMessages())
	}
}

// UpdateToolCallCompleted updates a tool call message to completed state
func (m *ChatModel) UpdateToolCallCompleted(index int, result string) {
	if index >= 0 && index < len(m.messages) {
		m.messages[index].ToolStatus = ToolStatusSuccess
		m.messages[index].ToolResult = result
		m.viewport.SetContent(m.renderMessages())
	}
}

// ToggleToolCallExpand toggles the expanded state of a completed tool call message
func (m *ChatModel) ToggleToolCallExpand(index int) {
	if index >= 0 && index < len(m.messages) {
		msg := &m.messages[index]
		if msg.Role == "tool_call" && msg.ToolStatus == ToolStatusSuccess && msg.ToolResult != "" {
			msg.Expanded = !msg.Expanded
			m.viewport.SetContent(m.renderMessages())
		}
	}
}

// FindToolCallAtLine finds the tool call message index at a given line position
func (m *ChatModel) FindToolCallAtLine(line int) int {
	currentLine := 0
	for i, msg := range m.messages {
		if msg.Role == "tool_call" {
			// Status line + args line + optional expanded result line
			lines := 1
			if msg.ToolArgs != "" {
				lines++
			}
			if msg.Expanded && msg.ToolStatus == ToolStatusSuccess && msg.ToolResult != "" {
				lines++
			}
			if line >= currentLine && line < currentLine+lines {
				return i
			}
			currentLine += lines
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
