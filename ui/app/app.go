package app

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/ui/components"
	"github.com/user/mini_code/ui/styles"
	"github.com/user/mini_code/util"
)

// ansiEscapeRe matches ANSI escape sequences for stripping
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripAnsi removes ANSI escape sequences from a string
func stripAnsi(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

// Focus states
type focusState int

const (
	focusInput focusState = iota
	focusSidebar
	focusChat
)

// Model is the main TUI application model
type Model struct {
	// Layout
	width  int
	height int
	ready  bool

	// Components
	chat    components.ChatModel
	input   components.InputModel
	sidebar components.SidebarModel

	// State
	sessions       []components.Session
	currentSession *components.Session
	pendingMessage string // message waiting for session creation
	isProcessing   bool
	focus          focusState

	// Backend
	client     *provider.ModelClient
	sessionMgr *session.SessionManager

	// Cancellation
	cancelFunc context.CancelFunc

	// Tool call tracking
	toolCallIndices map[string]int // maps tool call ID to message index

	// Token tracking
	currentTokens int
	maxTokens     int
	maxTurns      int

	// Compaction state
	isCompacting bool
}

// Messages
type (
	// WindowSizeMsg is sent when the window is resized
	WindowSizeMsg struct {
		Width  int
		Height int
	}

	// ResponseMsg is sent when a response is received
	ResponseMsg struct {
		Content string
		Err     error
	}

	// StreamingMsg is sent for streaming responses
	StreamingMsg struct {
		Content string
		Done    bool
	}

	// ToolCallDecidedEvent is sent when the agent decides to call a tool (before execution)
	ToolCallDecidedEvent struct {
		ID       string
		ToolName string
		Args     string
	}

	// ToolCallStartedEvent is sent when a tool call starts executing
	ToolCallStartedEvent struct {
		ID       string
		ToolName string
	}

	// ToolCallCompletedEvent is sent when a tool call completes
	ToolCallCompletedEvent struct {
		ID       string
		ToolName string
		Result   string
	}

	// AssistantReplyEvent is sent when assistant has a text reply (streaming, accumulated)
	AssistantReplyEvent struct {
		Content          string
		ReasoningContent string
	}
)

// Channel for tool call events
var toolCallChan chan interface{}

func init() {
	toolCallChan = make(chan interface{}, 100)
}

// waitForToolCallEvent creates a command that waits for a tool call event
func waitForToolCallEvent() tea.Cmd {
	return func() tea.Msg {
		event := <-toolCallChan
		return event
	}
}

// SendToolCallEvent sends a tool call event to the UI
func SendToolCallEvent(event interface{}) {
	toolCallChan <- event
}

// NewModel creates a new TUI model
func NewModel(client *provider.ModelClient, sessionMgr *session.SessionManager) Model {
	input := components.NewInputModel(nil)
	sessions := make([]components.Session, 0)

	// Load config for token limits
	cfg, _ := config.LoadConfig()
	maxTokens := 128000 // default context window
	if cfg != nil && cfg.Model.ContextWindow > 0 {
		maxTokens = cfg.Model.ContextWindow
		if cfg.Model.ReserveTokens > 0 {
			maxTokens -= cfg.Model.ReserveTokens
		}
	}

	// Load sessions from database - only main sessions (ParentID empty)
	if sessionMgr != nil {
		loadedSessions, err := sessionMgr.List("", "", 100, 0)
		if err == nil {
			for _, s := range loadedSessions {
				// Skip child sessions
				if s.ParentID() != "" {
					continue
				}
				// Create summary from first message or use title
				summary := s.Title()
				if len(s.Record().Messages) > 0 {
					// Get first user message as summary
					for _, msg := range s.Record().Messages {
						if msg.Role == "user" && len(msg.Content) > 0 {
							summary = msg.Content
							if len(summary) > 20 {
								summary = summary[:20] + "..."
							}
							break
						}
					}
				}
				sessions = append(sessions, components.Session{
					ID:    s.ID(),
					Title: summary,
				})
			}
		}
	}

	// Default maxTurns from agent config
	maxTurns := 999
	if cfg, err := agent.GetAgentConfig("build"); err == nil && cfg.MaxTurns > 0 {
		maxTurns = cfg.MaxTurns
	}

	// Initialize sidebar (no callbacks - use message-based approach)
	sidebar := components.NewSidebarModel(sessions)

	model := Model{
		chat:            components.NewChatModel(),
		input:           input,
		sidebar:         sidebar,
		sessions:        sessions,
		client:          client,
		sessionMgr:      sessionMgr,
		focus:           focusInput,
		toolCallIndices: make(map[string]int),
		maxTokens:       maxTokens,
		maxTurns:        maxTurns,
	}

	return model
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		m.input.Focus(),
	)
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

		// Layout: sidebar (25%) + main (75%)
		sidebarWidth := m.width / 4
		mainWidth := m.width - sidebarWidth

		// Update component sizes
		m.sidebar.SetWidth(sidebarWidth)

		// Chat: main area minus input(8 lines for textarea+border) minus status(1 line) minus margins
		chatHeight := m.height - 12 // header(2) + input(8) + status(1) + margins
		if chatHeight < 5 {
			chatHeight = 5
		}
		m.chat.SetSize(mainWidth-4, chatHeight)
		m.input.SetWidth(mainWidth - 4)

		return m, nil

	case tea.KeyMsg:
		// Global shortcuts (work regardless of focus)
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.isProcessing {
				// Cancel current processing
				m.isProcessing = false
				if m.cancelFunc != nil {
					m.cancelFunc()
					m.cancelFunc = nil
				}
				m.chat.AddErrorMessage(fmt.Errorf("interrupted"))
			}
			// Do NOT quit on Ctrl+C — user exits via window close
		case tea.KeyCtrlN:
			// New session
			return m, m.createNewSession()
		case tea.KeyCtrlL:
			// Clear chat
			m.chat.ClearMessages()
			return m, nil
		}

		// Handle F5 key for compact
		if msg.Type == tea.KeyF5 && !m.isProcessing && !m.isCompacting && m.currentSession != nil {
			m.isCompacting = true
			return m, m.startCompaction()
		}

		// Tab: cycle focus between components
		if msg.Type == tea.KeyTab {
			m.cycleFocus()
			return m, nil
		}

		// Route keyboard events based on focus
		switch m.focus {
		case focusInput:
			// Input handles Enter and all typing
			if !m.isProcessing {
				m.input, cmd = m.input.Update(msg)
				cmds = append(cmds, cmd)
			}
		case focusSidebar:
			// Sidebar handles arrow keys, Enter, Delete, F2
			// Only handle sidebar-specific keys, block others
			if msg.Type == tea.KeyUp || msg.Type == tea.KeyDown || msg.Type == tea.KeyEnter ||
				msg.Type == tea.KeyDelete || msg.Type == tea.KeyF2 {
				m.sidebar, cmd = m.sidebar.Update(msg)
				cmds = append(cmds, cmd)
			}
		case focusChat:
			// Chat viewport handles arrow keys for scrolling
			if msg.Type == tea.KeyUp || msg.Type == tea.KeyDown || msg.Type == tea.KeyPgUp ||
				msg.Type == tea.KeyPgDown || msg.Type == tea.KeyHome || msg.Type == tea.KeyEnd {
				m.chat, cmd = m.chat.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

		return m, tea.Batch(cmds...)

	case tea.MouseMsg:
		// Debug: log mouse events
		slog.Debug("mouse event", "action", msg.Action, "button", msg.Button, "x", msg.X, "y", msg.Y)

		// Handle mouse wheel scrolling — always consume to prevent terminal scrollback duplication
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			sidebarWidth := m.width / 4
			otherAreaHeight := m.height - 12 // height above input area
			if otherAreaHeight < 5 {
				otherAreaHeight = 5
			}

			if msg.X < sidebarWidth {
				// Mouse in sidebar area — consume event (sidebar has no scrollable viewport)
				return m, nil
			}

			// Calculate input area Y position
			inputStartY := otherAreaHeight + 2 // header(1) + chatHeight + 1

			if m.focus == focusInput && msg.Y >= inputStartY {
				// Mouse wheel in input area with input focus — route to input
				m.input, cmd = m.input.Update(msg)
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}

			// Main area — route to chat viewport for scrolling
			m.chat, cmd = m.chat.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		// Handle mouse click events (left button press)
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			sidebarWidth := m.width / 4
			otherAreaHeight := m.height - 12 // height above input area
			if otherAreaHeight < 5 {
				otherAreaHeight = 5
			}

			// Calculate input area Y position
			inputStartY := otherAreaHeight + 2 // header(1) + chatHeight + 1

			if msg.X < sidebarWidth {
				// Click in sidebar area — switch focus to sidebar and route click
				m.focus = focusSidebar
				m.sidebar, cmd = m.sidebar.Update(msg)
				cmds = append(cmds, cmd)
			} else if msg.Y >= inputStartY {
				// Click in input area — switch focus to input
				m.focus = focusInput
				m.input.Focus()
			} else {
				// Click in chat area — switch focus to chat
				m.focus = focusChat
				// Check if click is on a tool call message
				toolIdx := m.chat.FindToolCallAtLine(msg.Y - 2) // subtract header line
				if toolIdx >= 0 {
					m.chat.ToggleToolCallExpand(toolIdx)
				}
			}
		}

		return m, tea.Batch(cmds...)
	}

	// Handle session selected from sidebar
	if sessSelMsg, ok := msg.(components.SessionSelectedMsg); ok {
		// Load selected session messages into chat
		if m.sessionMgr != nil {
			sess, err := m.sessionMgr.Get(sessSelMsg.Session.ID)
			if err == nil && sess != nil {
				m.chat.ClearMessages()
				for _, histMsg := range sess.Record().Messages {
					role := histMsg.Role
					if role == "system" {
						continue
					}
					content := stripAnsi(histMsg.GetText())

					switch role {
					case "user":
						m.chat.AddMessage(components.Message{
							Role:    "user",
							Content: content,
						})
					case "assistant":
						// Assistant message may carry tool_calls
						if len(histMsg.ToolCalls) > 0 {
							// Show the assistant text first (if any)
							if content != "" {
								m.chat.AddMessage(components.Message{
									Role:             "assistant",
									Content:          content,
									ReasoningContent: histMsg.ReasoningContent,
								})
							}
							// Reconstruct each tool call as a completed tool_call message
							for _, tc := range histMsg.ToolCalls {
								m.chat.AddMessage(components.Message{
									Role:       "tool_call",
									ToolName:   tc.Function.Name,
									ToolArgs:   tc.Function.Arguments,
									ToolStatus: components.ToolStatusSuccess,
									ToolResult: "(from history)",
								})
							}
						} else {
							m.chat.AddMessage(components.Message{
								Role:             "assistant",
								Content:          content,
								ReasoningContent: histMsg.ReasoningContent,
							})
						}
					case "tool":
						// Tool result: show with tool name header
						toolName := histMsg.Name
						if toolName == "" {
							toolName = "unknown"
						}
						m.chat.AddMessage(components.Message{
							Role:     "tool",
							Content:  fmt.Sprintf("[%s] %s", toolName, content),
							ToolName: toolName,
						})
					default:
						m.chat.AddMessage(components.Message{
							Role:    role,
							Content: content,
						})
					}
				}
				// Update token count
				m.currentTokens = util.CountMessagesTokens(sess.Record().Messages)
			}
		}
		m.currentSession = &sessSelMsg.Session
		m.focus = focusInput
		m.input.Focus()
		return m, nil
	}

	// Handle session deleted from sidebar
	if sessDelMsg, ok := msg.(components.SessionDeletedMsg); ok {
		// Delete from database
		if m.sessionMgr != nil {
			m.sessionMgr.Delete(sessDelMsg.SessionID)
		}
		// Remove from local sessions list
		for i, s := range m.sessions {
			if s.ID == sessDelMsg.SessionID {
				m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
				break
			}
		}
		// Clear current session if it was deleted
		if m.currentSession != nil && m.currentSession.ID == sessDelMsg.SessionID {
			m.currentSession = nil
			m.chat.ClearMessages()
		}
		return m, nil
	}

	// Handle user message submission
	if submitMsg, ok := msg.(components.InputSubmitMsg); ok {
		// Create session if none exists
		if m.currentSession == nil {
			// Store the pending message and create session first
			m.pendingMessage = submitMsg.Content
			return m, m.createNewSession()
		}
		return m, m.processUserMessage(submitMsg.Content)
	}

	// Handle session created
	if sessMsg, ok := msg.(SessionCreatedMsg); ok {
		session := sessMsg.Session
		m.sessions = append(m.sessions, session)
		m.currentSession = &m.sessions[len(m.sessions)-1]
		m.sidebar.SetSessions(m.sessions)

		// Process pending message if any
		if m.pendingMessage != "" {
			pendingMsg := m.pendingMessage
			m.pendingMessage = ""
			return m, m.processUserMessage(pendingMsg)
		}
		return m, nil
	}

	// Handle start agent task (store cancel func, then dispatch actual task)
	if startMsg, ok := msg.(startAgentTaskMsg); ok {
		m.cancelFunc = startMsg.cancel
		return m, tea.Batch(
			func() tea.Msg {
				return agentTaskRunnerMsg{ctx: startMsg.ctx, text: startMsg.text, sessionID: startMsg.sessionID}
			},
			waitForToolCallEvent(),
		)
	}

	// Handle actual agent task execution - dispatch to goroutine to avoid blocking UI
	if runMsg, ok := msg.(agentTaskRunnerMsg); ok {
		agentRole := "build"
		workDir := "."
		sessionID := runMsg.sessionID
		text := runMsg.text
		ctx := runMsg.ctx
		// Store cancel func for Ctrl+C support — the goroutine below will clear it
		return m, func() tea.Msg {
			var result string
			var err error
			if sessionID != "" {
				// Try to continue existing session
				_, getErr := m.sessionMgr.Get(sessionID)
				if getErr == nil {
					// Session exists in DB, continue it
					result, err = m.sessionMgr.ContinueTask(ctx, sessionID, text, m.maxTurns)
				} else {
					// Session not in DB (local only), create new
					result, err = m.sessionMgr.RunTask(ctx, text, text, agentRole, workDir, m.maxTurns)
				}
			} else {
				// Create new session
				result, err = m.sessionMgr.RunTask(ctx, text, text, agentRole, workDir, m.maxTurns)
			}
			if err != nil {
				return ResponseMsg{Content: "", Err: err}
			}
			return ResponseMsg{Content: result}
		}
	}

	// Handle tool call decided event (agent chose to call a tool, not yet executed)
	if toolDecidedMsg, ok := msg.(ToolCallDecidedEvent); ok {
		idx := m.chat.AddToolCallDecided(toolDecidedMsg.ToolName, toolDecidedMsg.Args)
		m.toolCallIndices[toolDecidedMsg.ID] = idx
		return m, waitForToolCallEvent()
	}

	// Handle tool call started event
	if toolStartMsg, ok := msg.(ToolCallStartedEvent); ok {
		idx, found := m.toolCallIndices[toolStartMsg.ID]
		if !found {
			// Fallback: decided event was missed, create the entry now
			idx = m.chat.AddToolCallDecided(toolStartMsg.ToolName, "")
			m.toolCallIndices[toolStartMsg.ID] = idx
		}
		m.chat.UpdateToolCallRunning(idx)
		return m, waitForToolCallEvent()
	}

	// Handle tool call completed event
	if toolEndMsg, ok := msg.(ToolCallCompletedEvent); ok {
		idx, found := m.toolCallIndices[toolEndMsg.ID]
		if !found {
			// Fallback: earlier events were missed, create the entry now
			idx = m.chat.AddToolCallDecided(toolEndMsg.ToolName, "")
			m.toolCallIndices[toolEndMsg.ID] = idx
			m.chat.UpdateToolCallRunning(idx)
		}
		m.chat.UpdateToolCallCompleted(idx, toolEndMsg.Result)
		delete(m.toolCallIndices, toolEndMsg.ID)
		return m, waitForToolCallEvent()
	}

	// Handle assistant reply event
	if replyMsg, ok := msg.(AssistantReplyEvent); ok {
		// Add assistant text reply to chat
		m.chat.UpdateStreamingContent(replyMsg.Content, replyMsg.ReasoningContent)
		return m, waitForToolCallEvent()
	}

	// Handle user input
	if userInputMsg, ok := msg.(UserInputMsg); ok {
		m.chat.AddMessage(components.Message{
			Role:    "user",
			Content: userInputMsg.Content,
		})
		// Update token count for user message
		m.currentTokens += util.EstimateTokens(userInputMsg.Content) + 4
		m.isProcessing = true
		// Reset tool call tracking for the new run
		m.toolCallIndices = make(map[string]int)
		return m, m.processAgentResponse(userInputMsg.Content)
	}

	// Handle response
	if respMsg, ok := msg.(ResponseMsg); ok {
		m.isProcessing = false
		if respMsg.Err != nil {
			m.chat.AddErrorMessage(respMsg.Err)
		} else {
			m.chat.UpdateStreamingContent(respMsg.Content, "")
		}
		// Update token count from session
		if m.sessionMgr != nil && m.currentSession != nil {
			sess, err := m.sessionMgr.Get(m.currentSession.ID)
			if err == nil && sess != nil {
				m.currentTokens = util.CountMessagesTokens(sess.Record().Messages)
			}
		}
		return m, nil
	}

	// Handle compaction completed
	if compMsg, ok := msg.(CompactionCompletedMsg); ok {
		m.isCompacting = false
		m.isProcessing = false
		if compMsg.Err != nil {
			m.chat.AddErrorMessage(compMsg.Err)
		} else {
			// Update chat with compacted messages
			m.chat.ClearMessages()
			for _, histMsg := range compMsg.Messages {
				role := histMsg.Role
				if role == "system" {
					continue
				}
				content := stripAnsi(histMsg.GetText())
				switch role {
				case "user":
					m.chat.AddMessage(components.Message{
						Role:    "user",
						Content: content,
					})
				case "assistant":
					m.chat.AddMessage(components.Message{
						Role:             "assistant",
						Content:          content,
						ReasoningContent: histMsg.ReasoningContent,
					})
				}
			}
			// Update token count
			m.currentTokens = util.CountMessagesTokens(compMsg.Messages)
			m.chat.AddMessage(components.Message{
				Role:    "assistant",
				Content: "Context compacted successfully!",
			})
		}
		return m, nil
	}

	return m, tea.Batch(cmds...)
}

// cycleFocus moves focus to the next component
func (m *Model) cycleFocus() {
	switch m.focus {
	case focusInput:
		m.focus = focusSidebar
	case focusSidebar:
		m.focus = focusChat
	case focusChat:
		m.focus = focusInput
	}
}

// View renders the UI
func (m Model) View() string {
	if !m.ready {
		return styles.HeaderStyle.Render("Initializing Mini Code Agent...")
	}

	// Layout
	sidebarWidth := m.width / 4
	mainWidth := m.width - sidebarWidth

	// Header
	header := m.renderHeader()

	// Sidebar
	sidebar := m.sidebar.View()
	sidebarBox := lipgloss.NewStyle().
		Width(sidebarWidth).
		Height(m.height - 2).
		Render(sidebar)

	// Chat area
	chatView := m.chat.View()

	// Input area
	var inputView string
	if m.isProcessing {
		// Show cancel button when processing
		cancelBtn := lipgloss.NewStyle().
			Foreground(styles.AccentRed).
			Render("[Cancel (Ctrl+C)]")
		inputView = lipgloss.JoinHorizontal(lipgloss.Center,
			styles.InputStyle.Render("Processing..."),
			" ",
			cancelBtn,
		)
	} else {
		inputView = m.input.View()
	}

	// Compact button row (below input)
	compactRow := ""
	if m.currentTokens > 0 && m.currentSession != nil {
		if m.isCompacting {
			// Show compacting status
			compactingText := lipgloss.NewStyle().
				Foreground(styles.AccentYellow).
				Render("[F5: Compact] 压缩中...")
			compactRow = compactingText
		} else {
			compactBtn := lipgloss.NewStyle().
				Foreground(styles.AccentYellow).
				Render("[F5: Compact]")
			compactRow = compactBtn
		}
	}

	// Status bar
	statusBar := m.renderStatusBar()

	// Combine main content: chat + input + compact row + status
	mainContent := lipgloss.JoinVertical(lipgloss.Left,
		chatView,
		inputView,
		compactRow,
		statusBar,
	)

	mainBox := lipgloss.NewStyle().
		Width(mainWidth).
		Height(m.height - 2).
		Render(mainContent)

	// Combine sidebar and main
	content := lipgloss.JoinHorizontal(lipgloss.Top, sidebarBox, mainBox)

	return lipgloss.JoinVertical(lipgloss.Left, header, content)
}

// renderHeader renders the header bar
func (m Model) renderHeader() string {
	title := styles.HeaderStyle.Render("Mini Code")
	separator := styles.DividerStyle.Render(strings.Repeat("─", m.width-10))
	return lipgloss.JoinHorizontal(lipgloss.Center, title, separator)
}

// renderStatusBar renders the status bar
func (m Model) renderStatusBar() string {
	status := "Ready"
	if m.isProcessing {
		status = "Processing..."
	}

	sessionInfo := "No session"
	if m.currentSession != nil {
		sessionInfo = m.currentSession.Title
	}

	// Calculate token usage
	tokenInfo := ""
	if m.maxTokens > 0 {
		percentage := 0.0
		if m.currentTokens > 0 {
			percentage = float64(m.currentTokens) / float64(m.maxTokens) * 100
		}
		tokenInfo = fmt.Sprintf("Tokens: %d/%d (%.1f%%)", m.currentTokens, m.maxTokens, percentage)
	}

	// Show focus indicator
	focusHint := "[Tab: Switch focus]"
	switch m.focus {
	case focusInput:
		focusHint = "[Input] Tab:Switch"
	case focusSidebar:
		focusHint = "[Sidebar] Tab:Switch | Enter:Select | Del:Delete | F2:Rename"
	case focusChat:
		focusHint = "[Chat] Tab:Switch | ↑↓:Scroll"
	}

	return styles.StatusBarStyle.Render(
		fmt.Sprintf(" %s | %s | %s | %s", status, sessionInfo, tokenInfo, focusHint),
	)
}

// createNewSession creates a new session in database
func (m Model) createNewSession() tea.Cmd {
	return func() tea.Msg {
		// Create session in database
		if m.sessionMgr != nil {
			sess, err := m.sessionMgr.CreateSession("New Chat", "build", ".", nil)
			if err == nil && sess != nil {
				// Type assert to *session.Session to get ID
				if sessWithID, ok := sess.(*session.Session); ok {
					return SessionCreatedMsg{Session: components.Session{
						ID:    sessWithID.ID(),
						Title: "New Chat",
					}}
				}
			}
		}

		// Fallback: create local session (should not happen)
		sessionID := fmt.Sprintf("session_%d", time.Now().UnixNano())
		return SessionCreatedMsg{Session: components.Session{
			ID:    sessionID,
			Title: fmt.Sprintf("Chat %d", len(m.sessions)+1),
		}}
	}
}

// SessionCreatedMsg is sent when a new session is created
type SessionCreatedMsg struct {
	Session components.Session
}

// processUserMessage creates a command that processes user input
func (m Model) processUserMessage(text string) tea.Cmd {
	return func() tea.Msg {
		return UserInputMsg{Content: text}
	}
}

// processAgentResponse creates a command that gets agent response
func (m Model) processAgentResponse(text string) tea.Cmd {
	return func() tea.Msg {
		// Use session manager to run task
		if m.sessionMgr != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			// Store cancel func via a message so the model can call it
			defer func() {
				// Note: cancel will be called by Ctrl+C handler or timeout
			}()

			// Determine session ID: empty for new session, non-empty to continue existing
			sessionID := ""
			if m.currentSession != nil {
				sessionID = m.currentSession.ID
			}

			// Send a message to store the cancel func, then run the task
			// We use a helper to avoid race conditions
			return startAgentTaskMsg{
				ctx:       ctx,
				cancel:    cancel,
				text:      text,
				sessionID: sessionID,
			}
		}

		// Fallback: echo back
		response := fmt.Sprintf("Echo: %s", text)
		return ResponseMsg{Content: response}
	}
}

// startAgentTaskMsg is sent to store the cancel func then run the agent
type startAgentTaskMsg struct {
	ctx       context.Context
	cancel    context.CancelFunc
	text      string
	sessionID string // empty for new session, non-empty to continue existing
}

// agentTaskRunnerMsg is sent to actually run the task after cancel is stored
type agentTaskRunnerMsg struct {
	ctx       context.Context
	text      string
	sessionID string // empty for new session, non-empty to continue existing
}

// CompactionCompletedMsg is sent when compaction completes
type CompactionCompletedMsg struct {
	Messages []provider.Message
	Err      error
}

// startCompaction starts the compaction process
func (m Model) startCompaction() tea.Cmd {
	return func() tea.Msg {
		if m.sessionMgr == nil || m.currentSession == nil {
			return CompactionCompletedMsg{Err: fmt.Errorf("no session")}
		}

		sess, err := m.sessionMgr.Get(m.currentSession.ID)
		if err != nil || sess == nil {
			return CompactionCompletedMsg{Err: fmt.Errorf("session not found")}
		}

		messages := sess.Record().Messages
		if len(messages) == 0 {
			return CompactionCompletedMsg{Err: fmt.Errorf("no messages to compact")}
		}

		// Create compactor and run compaction
		compactor := session.NewContextCompactor(m.client)
		newMessages, err := compactor.Compact(context.Background(), messages)
		if err != nil {
			return CompactionCompletedMsg{Err: err}
		}

		// Update session in DB
		sess.Record().Messages = newMessages
		// Update title from first user message
		for _, msg := range newMessages {
			if msg.Role == "user" && len(msg.Content) > 0 {
				sess.Record().Title = msg.Content
				if len(sess.Record().Title) > 50 {
					sess.Record().Title = sess.Record().Title[:50] + "..."
				}
				break
			}
		}
		m.sessionMgr.GetStore().Update(sess.Record())

		return CompactionCompletedMsg{Messages: newMessages}
	}
}

// UserInputMsg is sent when user submits input
type UserInputMsg struct {
	Content string
}

// Run starts the TUI application
func Run(client *provider.ModelClient, sessionMgr *session.SessionManager) {
	m := NewModel(client, sessionMgr)

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running TUI: %v\n", err)
	}
}
