// Package dingtalk implements the DingTalk channel adapter for mini_code.
//
// It uses the DingTalk Stream mode (outbound WebSocket, no public IP or
// callback URL required):
//
//  1. The client opens a gateway connection with the app's
//     ClientID (AppKey) / ClientSecret (AppSecret).
//  2. DingTalk pushes bot messages over the WebSocket; each message is
//     forwarded into the message hub as an inbound user message whose
//     SessionKey is the DingTalk conversation id. The hub binds the key to a
//     session (creating one on first contact), so every DingTalk
//     conversation maps to exactly one mini_code session.
//  3. Produced assistant replies are sent back through the conversation's
//     session webhook.
package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/util"
)

const (
	gatewayURL  = "https://api.dingtalk.com/v1.0/gateway/connections/open"
	botTopic    = "/v1.0/im/bot/messages/get"
	userAgent   = "mini_code/1.0"
	maxReplyLen = 3500 // safety margin for DingTalk message size
)

// DingTalk open-platform endpoints. Package variables so tests can point
// them at local servers.
var (
	accessTokenURL  = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
	downloadFileURL = "https://api.dingtalk.com/v1.0/robot/messageFiles/download"
)

// Client is the DingTalk stream channel client.
type Client struct {
	cfg    config.DingTalkConfig
	hub    *channels.Hub
	http   *http.Client
	dlHTTP *http.Client // long timeout for file downloads

	tokenMu  sync.Mutex
	tokenVal string
	tokenExp time.Time

	mu       sync.Mutex
	webhooks map[string]webhookInfo // conversationId -> sessionWebhook
	tasks    map[string]*taskState  // conversationId -> running-task progress state
	seenMsgs map[string]time.Time   // msgId -> first seen (dedupe window)
}

// webhookInfo is a learned per-conversation reply URL with its validity.
type webhookInfo struct {
	url     string
	expires time.Time // zero = unknown
}

// taskState tracks one DingTalk conversation's running tasks for the
// two-stage feedback (ack + periodic progress pings).
type taskState struct {
	active    int       // submitted tasks not yet terminated
	startedAt time.Time // when the first active task started
	lastSent  time.Time // last outgoing message (ack / progress / final)
	lastAct   string    // latest tool activity, "" = still thinking
	pings     int       // progress pings sent for this run
}

// New creates a DingTalk channel client bound to the hub.
func New(cfg config.DingTalkConfig, hub *channels.Hub) *Client {
	return &Client{
		cfg:      cfg,
		hub:      hub,
		http:     &http.Client{Timeout: 30 * time.Second},
		dlHTTP:   &http.Client{Timeout: 5 * time.Minute},
		webhooks: make(map[string]webhookInfo),
		tasks:    make(map[string]*taskState),
		seenMsgs: make(map[string]time.Time),
	}
}

// Start runs the channel until ctx is done. It reconnects automatically with
// exponential backoff when the stream drops. It blocks; call it in a
// goroutine or as the main loop.
func (c *Client) Start(ctx context.Context) error {
	if c.cfg.AppKey == "" || c.cfg.AppSecret == "" {
		return fmt.Errorf("dingtalk: app_key/app_secret not configured")
	}

	// Consume outbound hub messages and reply on DingTalk.
	out := c.hub.Subscribe(channels.ChannelDingTalk)
	go c.consume(ctx, out)
	go c.progressLoop(ctx)

	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("dingtalk stream disconnected, reconnecting", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// run maintains one live WebSocket connection.
func (c *Client) run(ctx context.Context) error {
	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	slog.Info("dingtalk stream connected")
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Idle watchdog: the gateway is silent for long stretches when no
		// messages flow, so this must stay well above its keep-alive
		// cadence (95s caused a reconnect loop; 5m detects dead links).
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := c.handleFrame(ctx, conn, raw); err != nil {
			slog.Warn("dingtalk: handling frame failed", "err", err)
		}
	}
}

// connect opens the gateway connection and dials the returned WebSocket
// endpoint.
func (c *Client) connect(ctx context.Context) (*websocket.Conn, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"clientId":     c.cfg.AppKey,
		"clientSecret": c.cfg.AppSecret,
		"subscriptions": []map[string]string{
			{"type": "CALLBACK", "topic": botTopic},
		},
		"ua": userAgent,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dingtalk gateway: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dingtalk gateway: HTTP %d", resp.StatusCode)
	}
	var gw struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gw); err != nil {
		return nil, fmt.Errorf("dingtalk gateway: bad response: %w", err)
	}
	if gw.Endpoint == "" || gw.Ticket == "" {
		return nil, fmt.Errorf("dingtalk gateway: missing endpoint/ticket")
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	wsURL := gw.Endpoint + "?ticket=" + url.QueryEscape(gw.Ticket)
	conn, resp, err := dialer.DialContext(ctx, wsURL, http.Header{"User-Agent": []string{userAgent}})
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("dingtalk websocket dial: %w", err)
	}
	return conn, nil
}

// dtFrame is one frame received from the DingTalk stream gateway.
type dtFrame struct {
	SpecVersion string            `json:"specVersion"`
	Type        string            `json:"type"` // SYSTEM | CALLBACK | EVENT
	Headers     map[string]string `json:"headers"`
	Data        string            `json:"data"`
}

// dtAck is the acknowledgement frame sent back to the gateway.
type dtAck struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

func (c *Client) handleFrame(ctx context.Context, conn *websocket.Conn, raw []byte) error {
	var frame dtFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return fmt.Errorf("bad frame: %w", err)
	}
	msgID := frame.Headers["messageId"]

	switch frame.Type {
	case "SYSTEM":
		// Keep-alive ping: answer so the gateway keeps the connection.
		c.sendAck(conn, msgID)
	case "CALLBACK":
		if frame.Headers["topic"] == botTopic {
			c.handleBotMessage(frame.Data)
		}
		c.sendAck(conn, msgID)
	default:
		c.sendAck(conn, msgID)
	}
	return nil
}

func (c *Client) sendAck(conn *websocket.Conn, msgID string) {
	ack := dtAck{
		Code:    200,
		Headers: map[string]string{"messageId": msgID, "contentType": "application/json"},
		Message: "OK",
		Data:    "{}",
	}
	b, _ := json.Marshal(ack)
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		slog.Warn("dingtalk: ack failed", "err", err)
	}
}

// botMessage is the payload of a /v1.0/im/bot/messages/get callback.
type botMessage struct {
	ConversationId    string `json:"conversationId"`
	ConversationType  string `json:"conversationType"` // "1" single chat, "2" group
	ConversationTitle string `json:"conversationTitle"`
	SenderStaffId     string `json:"senderStaffId"`
	SenderId          string `json:"senderId"`
	SenderNick        string `json:"senderNick"`
	ChatbotUserId     string `json:"chatbotUserId"`
	MsgId             string `json:"msgId"`
	Msgtype           string `json:"msgtype"`
	Text              struct {
		Content string `json:"content"`
	} `json:"text"`
	// File messages carry a top-level "content" object (text messages do
	// not), parsed lazily into fileContent.
	FileData              json.RawMessage `json:"content"`
	SessionWebhook        string          `json:"sessionWebhook"`
	SessionWebhookExpired int64           `json:"sessionWebhookExpiredTime"`
	RobotCode              string          `json:"robotCode"`
}

// fileContent is the payload of a file message.
type fileContent struct {
	DownloadCode string `json:"downloadCode"`
	FileName     string `json:"fileName"`
}

// handleBotMessage converts one pushed DingTalk bot message into an inbound
// hub message.
func (c *Client) handleBotMessage(raw string) {
	var msg botMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		slog.Warn("dingtalk: bad bot payload", "err", err)
		return
	}

	c.mu.Lock()
	if _, dup := c.seenMsgs[msg.MsgId]; dup {
		c.mu.Unlock()
		return
	}
	c.seenMsgs[msg.MsgId] = time.Now()
	c.pruneSeenLocked()
	if msg.SessionWebhook != "" && msg.ConversationId != "" {
		exp := time.Time{}
		if msg.SessionWebhookExpired > 0 {
			exp = time.UnixMilli(msg.SessionWebhookExpired)
		}
		c.webhooks[msg.ConversationId] = webhookInfo{url: msg.SessionWebhook, expires: exp}
	}
	c.mu.Unlock()

	if len(c.cfg.AllowStaff) > 0 &&
		!slices.Contains(c.cfg.AllowStaff, msg.SenderStaffId) &&
		!slices.Contains(c.cfg.AllowStaff, msg.SenderId) {
		slog.Warn("dingtalk: message from unauthorized sender dropped", "sender", msg.SenderStaffId)
		return
	}

	switch msg.Msgtype {
	case "", "text":
		text := strings.TrimSpace(msg.Text.Content)
		if text == "" {
			return
		}
		slog.Info("dingtalk: inbound message", "conversation", msg.ConversationId, "sender", msg.SenderNick, "chars", len([]rune(text)))
		c.hub.Submit(channels.Message{
			ID:         util.RandomHex(12),
			Kind:       channels.KindUser,
			Channel:    channels.ChannelDingTalk,
			SessionKey: msg.ConversationId, // conversation -> session binding; unknown conversation creates a new session
			Content:    text,
			CreatedAt:  time.Now(),
		})
		// Two-stage feedback: acknowledge immediately, then ping progress
		// periodically while the task runs (see progressLoop).
		c.noteTaskStart(msg.ConversationId)
		c.replyText(msg.ConversationId, "✅ 已收到，正在处理…")
	case "file":
		var fc fileContent
		if err := json.Unmarshal(msg.FileData, &fc); err != nil || fc.DownloadCode == "" {
			c.replyText(msg.ConversationId, "（无法识别的文件消息）")
			return
		}
		slog.Info("dingtalk: inbound file", "conversation", msg.ConversationId, "sender", msg.SenderNick, "file", fc.FileName)
		// Downloads can take a while: never block the stream read loop.
		go c.handleFileMessage(msg, fc)
	default:
		c.replyText(msg.ConversationId, "（暂只支持文本和文件消息）")
	}
}

// handleFileMessage downloads an uploaded file into the conversation's
// session workspace and feeds the agent a message pointing at the saved file.
func (c *Client) handleFileMessage(msg botMessage, fc fileContent) {
	file := safeFileName(fc.FileName)
	c.replyText(msg.ConversationId, fmt.Sprintf("📎 收到文件 %s，正在下载…", file))

	workDir := c.hub.WorkDirFor(msg.ConversationId)
	if workDir == "" {
		workDir = "."
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		c.replyText(msg.ConversationId, "❌ 无法创建工作目录，文件未接收")
		slog.Error("dingtalk: create workspace failed", "dir", workDir, "err", err)
		return
	}
	dest := filepath.Join(workDir, file)

	size, err := c.saveFile(fc.DownloadCode, dest)
	if err != nil {
		slog.Error("dingtalk: file download failed", "file", file, "err", err)
		c.replyText(msg.ConversationId, fmt.Sprintf("❌ 文件 %s 下载失败，请重新发送", file))
		return
	}
	slog.Info("dingtalk: file saved", "file", dest, "bytes", size)

	c.noteTaskStart(msg.ConversationId)
	c.hub.Submit(channels.Message{
		ID:         util.RandomHex(12),
		Kind:       channels.KindUser,
		Channel:    channels.ChannelDingTalk,
		SessionKey: msg.ConversationId,
		Content:    fmt.Sprintf("用户上传了文件 %s，已保存到工作区 %s，请查看并处理。", file, dest),
		CreatedAt:  time.Now(),
	})
	c.replyText(msg.ConversationId, fmt.Sprintf("✅ 文件已保存（%s），开始处理…", humanSize(size)))
}

// safeFileName reduces an uploaded file name to a plain file name that
// cannot escape the session workspace (no path components, no leading dots).
func safeFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Trim(name, ".")
	if name == "" {
		name = "file"
	}
	if r := []rune(name); len(r) > 150 {
		name = string(r[:150])
	}
	return name
}

// accessToken returns a cached DingTalk access token, refreshing it when it
// is missing or about to expire.
func (c *Client) accessToken() (string, error) {
	c.tokenMu.Lock()
	if c.tokenVal != "" && time.Now().Before(c.tokenExp.Add(-60*time.Second)) {
		v := c.tokenVal
		c.tokenMu.Unlock()
		return v, nil
	}
	c.tokenMu.Unlock()

	body, _ := json.Marshal(map[string]string{
		"appKey":    c.cfg.AppKey,
		"appSecret": c.cfg.AppSecret,
	})
	resp, err := c.http.Post(accessTokenURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("get access token: bad response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("get access token: HTTP %d", resp.StatusCode)
	}
	exp := time.Now().Add(time.Hour)
	if out.ExpireIn > 0 {
		exp = time.Now().Add(time.Duration(out.ExpireIn) * time.Second)
	}
	c.tokenMu.Lock()
	c.tokenVal, c.tokenExp = out.AccessToken, exp
	c.tokenMu.Unlock()
	return out.AccessToken, nil
}

// saveFile downloads one robot file (by its downloadCode) into dest and
// returns the size in bytes.
func (c *Client) saveFile(downloadCode, dest string) (int64, error) {
	token, err := c.accessToken()
	if err != nil {
		return 0, err
	}
	robotCode := c.cfg.RobotCode
	if robotCode == "" {
		robotCode = c.cfg.AppKey
	}

	// 1) exchange the downloadCode for a temporary download URL.
	body, _ := json.Marshal(map[string]string{
		"downloadCode": downloadCode,
		"robotCode":    robotCode,
	})
	req, err := http.NewRequest(http.MethodPost, downloadFileURL, strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("get download url: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("get download url: bad response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.DownloadURL == "" {
		return 0, fmt.Errorf("get download url: HTTP %d", resp.StatusCode)
	}

	// 2) stream the file from the temporary URL.
	dl, err := c.dlHTTP.Get(out.DownloadURL)
	if err != nil {
		return 0, fmt.Errorf("download file: %w", err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download file: HTTP %d", dl.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	size, err := io.Copy(f, dl.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest) // do not leave a partial file behind
		return 0, fmt.Errorf("download file: %w", err)
	}
	return size, nil
}

// humanSize renders a byte count for chat display.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGE"[exp])
}

// pruneSeenLocked drops dedupe entries older than 10 minutes.
func (c *Client) pruneSeenLocked() {
	cutoff := time.Now().Add(-10 * time.Minute)
	for id, t := range c.seenMsgs {
		if t.Before(cutoff) {
			delete(c.seenMsgs, id)
		}
	}
}

// consume delivers produced hub messages back to DingTalk.
func (c *Client) consume(ctx context.Context, out <-chan channels.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-out:
			if !ok {
				return
			}
			switch m.Kind {
			case channels.KindToolStart:
				c.noteActivity(m.SessionKey, m.ToolName, m.ToolArgs)
			case channels.KindAssistant:
				c.replyText(m.SessionKey, m.Content)
			case channels.KindError:
				c.replyText(m.SessionKey, "⚠️ 任务执行失败："+truncate(m.Content, 500))
				c.noteTaskEnd(m.SessionKey)
			case channels.KindStatus:
				// task_done / interrupted terminate a task (each task
				// produces exactly one of them); assistant replies do not,
				// a successful task sends assistant + task_done.
				if m.Status == channels.StatusTaskDone || m.Status == channels.StatusInterrupted {
					c.noteTaskEnd(m.SessionKey)
				}
			}
		}
	}
}

// replyText sends a markdown reply through the conversation's session webhook.
func (c *Client) replyText(conversationID, text string) {
	if conversationID == "" || text == "" {
		return
	}
	c.mu.Lock()
	wh, ok := c.webhooks[conversationID]
	if ok && !wh.expires.IsZero() && time.Now().After(wh.expires) {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		// The webhook is learned from inbound messages; after a process
		// restart the first reply may be dropped until the next inbound
		// message refreshes it.
		slog.Warn("dingtalk: no session webhook for conversation, reply skipped", "conversation", conversationID)
		return
	}

	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": "Mini Code",
			"text":  truncate(text, maxReplyLen),
		},
	}
	b, _ := json.Marshal(payload)
	resp, err := c.http.Post(wh.url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		slog.Warn("dingtalk: reply failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("dingtalk: reply failed", "status", resp.StatusCode)
	}
}

// --- Periodic progress pings (two-stage feedback) ---
//
// After the acknowledgement, a running task pings the conversation with
// progress at most once per progress interval (DingTalkConfig.ProgressInterval,
// default 10s). After progressCap pings the effective interval stretches to
// one minute so very long tasks do not flood the chat.

const (
	progressCap  = 30
	progressSlow = time.Minute
)

// noteTaskStart marks one more task of the conversation as running.
func (c *Client) noteTaskStart(conversationID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.tasks[conversationID]
	if st == nil {
		st = &taskState{startedAt: time.Now(), lastSent: time.Now()}
		c.tasks[conversationID] = st
	}
	st.active++
	st.lastSent = time.Now() // the acknowledgement sent right after counts
}

// noteActivity records the latest tool activity for progress display.
func (c *Client) noteActivity(conversationID, toolName, toolArgs string) {
	if conversationID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.tasks[conversationID]
	if st == nil || st.active <= 0 {
		return
	}
	if act := activityText(toolName, toolArgs); act != "" {
		st.lastAct = act
	}
}

// noteTaskEnd marks one task of the conversation as terminated.
func (c *Client) noteTaskEnd(conversationID string) {
	if conversationID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.tasks[conversationID]
	if st == nil {
		return
	}
	st.active--
	if st.active <= 0 {
		delete(c.tasks, conversationID)
	}
}

// progressLoop pings running tasks with progress on a fixed ticker.
func (c *Client) progressLoop(ctx context.Context) {
	interval := c.progressInterval()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.tickProgress(interval)
		}
	}
}

// progressInterval resolves the ping interval: negative = disabled, 0 (unset)
// = default 10s.
func (c *Client) progressInterval() time.Duration {
	switch {
	case c.cfg.ProgressInterval < 0:
		return 0
	case c.cfg.ProgressInterval == 0:
		return 10 * time.Second
	default:
		return time.Duration(c.cfg.ProgressInterval) * time.Second
	}
}

// tickProgress sends one progress ping per active conversation whose last
// outgoing message is old enough.
func (c *Client) tickProgress(interval time.Duration) {
	now := time.Now()
	c.mu.Lock()
	var pings []struct {
		conv string
		text string
	}
	for conv, st := range c.tasks {
		limit := interval
		if st.pings >= progressCap {
			limit = progressSlow
		}
		if st.active <= 0 || now.Sub(st.lastSent) < limit {
			continue
		}
		st.lastSent = now
		st.pings++
		act := st.lastAct
		if act == "" {
			act = "模型思考中…"
		}
		pings = append(pings, struct {
			conv string
			text string
		}{conv, fmt.Sprintf("⏳ 已运行 %s · %s", now.Sub(st.startedAt).Round(time.Second), act)})
	}
	c.mu.Unlock()

	for _, p := range pings {
		c.replyText(p.conv, p.text)
	}
}

// activityText renders a one-line "what is it doing" from a tool event.
func activityText(name, argsJSON string) string {
	if name == "" {
		return ""
	}
	text := name
	if argsJSON != "" {
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(argsJSON), &args); err == nil {
			for _, k := range []string{"command", "path", "description", "prompt", "working_dir"} {
				if s, ok := args[k].(string); ok && strings.TrimSpace(s) != "" {
					text += " " + s
					break
				}
			}
		}
	}
	return truncate(text, 60)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
