package session


import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

const (
	MaxNoteChars = 6000
	MaxWorkerNameRunes = 40
	MaxWorkerSummaryRunes = 400
	MaxWorkers = 5
)

type WorkerService struct {
	store *SessionStore
	mgr   *SessionManager
	mu    sync.Mutex
	maxWorkers atomic.Int64
}

func NewWorkerService(store *SessionStore, mgr *SessionManager) *WorkerService {
	w := &WorkerService{store: store, mgr: mgr}
	w.maxWorkers.Store(MaxWorkers)
	return w
}

func (w *WorkerService) SetMaxWorkers(n int) {
	if n < 1 || n > 100 {
		n = MaxWorkers
	}
	w.maxWorkers.Store(int64(n))
}

func (w *WorkerService) maxLimit() int {
	if n := int(w.maxWorkers.Load()); n >= 1 {
		return n
	}
	return MaxWorkers
}


func (w *WorkerService) List() ([]WorkerRecord, error) { return w.store.ListWorkers() }

func (w *WorkerService) Get(workerID string) (*WorkerRecord, error) {
	return w.store.GetWorker(workerID)
}

func (w *WorkerService) Joinable() ([]WorkerRecord, error) {
	all, err := w.store.ListWorkers()
	if err != nil {
		return nil, err
	}
	out := make([]WorkerRecord, 0, len(all))
	for _, rec := range all {
		if rec.Builtin {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}


func (w *WorkerService) Create(name, summary, baseDir, copyFrom string) (*WorkerRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("worker 名称不能为空")
	}
	if len([]rune(name)) > MaxWorkerNameRunes {
		return nil, fmt.Errorf("worker 名称过长(最多 %d 字)", MaxWorkerNameRunes)
	}
	if err := validateWorkerName(name); err != nil {
		return nil, err
	}
	summary = strings.TrimSpace(summary)
	if len([]rune(summary)) > MaxWorkerSummaryRunes {
		return nil, fmt.Errorf("worker 摘要过长(最多 %d 字)", MaxWorkerSummaryRunes)
	}
	if existing, err := w.store.GetWorkerByName(name); err == nil && existing != nil {
		return nil, fmt.Errorf("已经有一个同名 worker:%s(id %s)。要加入它请用 join;要换个名字再来。", name, existing.WorkerID)
	}
	if n, err := w.store.countWorkers(); err != nil {
		return nil, err
	} else if n >= w.maxLimit() {
		return nil, fmt.Errorf("worker 最多 %d 个(固定的「%s」也占一格),先删一个再建", w.maxLimit(), WorkerOtherName)
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("当前会话没有工作目录,无法创建 worker 的共享目录")
	}
	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("基础目录不存在或不是目录: %s", baseDir)
	}

	dir := uniqueWorkerDir(baseDir, "worker-"+workerSlug(name))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建工作目录失败: %w", err)
	}
	if copyFrom != "" {
		if msg, err := seedFrom(dir, copyFrom); err != nil {
			return nil, fmt.Errorf("复制起点目录失败: %w", err)
		} else if msg != "" {
			slog.Info("worker workspace seeded", "dir", dir, "from", msg)
		}
	}
	rec := &WorkerRecord{Name: name, Summary: summary, WorkDir: dir}
	if err := w.store.CreateWorker(rec); err != nil {
		return nil, err
	}
	slog.Info("worker created", "worker", rec.WorkerID, "name", name, "dir", dir)
	return rec, nil
}

func validateWorkerName(name string) error {
	if strings.ContainsAny(name, "/\\:*?\"<>|") {
		return fmt.Errorf("worker 名称不能包含路径分隔符或保留字符:/ \\ : * ? \" < > |")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("worker 名称不能包含换行或控制字符")
		}
	}
	if name == "." || name == ".." {
		return fmt.Errorf("worker 名称不能是 . 或 ..")
	}
	return nil
}

func (w *WorkerService) UpdateSummary(workerID, summary string) error {
	summary = strings.TrimSpace(summary)
	if len([]rune(summary)) > MaxWorkerSummaryRunes {
		return fmt.Errorf("worker 摘要过长(最多 %d 字)", MaxWorkerSummaryRunes)
	}
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在: %s", workerID)
	}
	rec.Summary = summary
	return w.store.UpdateWorker(rec)
}

func (w *WorkerService) Rename(workerID, newName string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("worker 名称不能为空")
	}
	if len([]rune(newName)) > MaxWorkerNameRunes {
		return fmt.Errorf("worker 名称过长(最多 %d 字)", MaxWorkerNameRunes)
	}
	if err := validateWorkerName(newName); err != nil {
		return err
	}
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在: %s", workerID)
	}
	if IsReservedWorker(workerID) {
		return fmt.Errorf("「%s」是固定的兜底 worker,名字写死了", rec.Name)
	}
	if rec.Builtin {
		return fmt.Errorf("%s 是内置分桶,不是工作线,不能改名", rec.Name)
	}
	if existing, err := w.store.GetWorkerByName(newName); err == nil && existing != nil && existing.WorkerID != workerID {
		return fmt.Errorf("已经有一个同名 worker:%s(id %s)。换个名字,或者直接加入那个。", newName, existing.WorkerID)
	}
	if rec.Name == newName {
		return nil
	}
	old := rec.Name
	rec.Name = newName
	if err := w.store.UpdateWorker(rec); err != nil {
		return err
	}
	n := 0
	if ids, err := w.store.ListWorkerSessions(workerID); err == nil {
		for _, sid := range ids {
			if sess, err := w.mgr.Get(sid); err == nil && sess != nil {
				sess.setWorkerNameLabel(newName)
				n++
				continue
			}
			if err := w.store.RelabelWorkerSession(sid, workerID, newName); err == nil {
				n++
			}
		}
	}
	slog.Info("worker renamed", "worker", workerID, "from", old, "to", newName, "relabeled", n, "dir", rec.WorkDir)
	return nil
}

func (w *WorkerService) Delete(workerID string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return 0, err
	}
	if rec == nil {
		return 0, fmt.Errorf("worker 不存在: %s", workerID)
	}
	if IsReservedWorker(workerID) {
		return 0, fmt.Errorf("「%s」是固定的兜底 worker,不能删除", rec.Name)
	}
	ids, err := w.store.ListWorkerSessions(workerID)
	if err != nil {
		return 0, err
	}
	for _, sid := range ids {
		if err := w.mgr.Delete(sid); err != nil {
			return len(ids), fmt.Errorf("删除会话 %s 失败: %w", sid, err)
		}
	}
	if err := w.store.DeleteWorker(workerID); err != nil {
		return len(ids), err
	}
	slog.Info("worker deleted", "worker", workerID, "name", rec.Name, "sessions", len(ids), "dir_kept", rec.WorkDir)
	return len(ids), nil
}


func (w *WorkerService) sessionOf(sessionID string) (*Session, error) {
	sess, err := w.mgr.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}
	return sess, nil
}

func (w *WorkerService) Join(sessionID, workerID string) (*WorkerRecord, error) {
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("worker 不存在: %s", workerID)
	}
	if rec.Builtin {
		return nil, fmt.Errorf("%s 是内置分桶,不能加入", rec.Name)
	}
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return nil, err
	}
	if err := w.attach(sess, rec); err != nil {
		return nil, err
	}
	slog.Info("session joined worker", "session", sessionID, "worker", rec.WorkerID, "dir", rec.WorkDir)
	return rec, nil
}

func (w *WorkerService) attach(sess *Session, rec *WorkerRecord) error {
	if sess == nil {
		return fmt.Errorf("会话不存在")
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在")
	}
	name, dir := rec.Name, rec.WorkDir
	if rec.Builtin {
		name, dir = "", ""
	}
	sess.SetWorkerMembership(rec.WorkerID, name, dir)
	return nil
}

func (w *WorkerService) Leave(sessionID string) error {
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return err
	}
	sess.SetWorkerMembership(WorkerOtherID, "", "")
	slog.Info("session left worker", "session", sessionID, "to", WorkerOtherID)
	return nil
}

func (w *WorkerService) Move(sessionID, bucket string) error {
	if bucket == WorkerUnassignedID || bucket == WorkerAllID {
		return w.Leave(sessionID)
	}
	if bucket == WorkerArchivedID {
		if err := w.store.ensureArchivedBucket(); err != nil {
			return err
		}
	}
	rec, err := w.store.GetWorker(bucket)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在: %s", bucket)
	}
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return err
	}
	if err := w.attach(sess, rec); err != nil {
		return err
	}
	slog.Info("session moved", "session", sessionID, "worker", rec.WorkerID, "name", rec.Name)
	return nil
}

func (m *SessionManager) MoveToWorker(sessionID, bucket string) error {
	w := m.Workers()
	if w == nil {
		return fmt.Errorf("worker 服务未启用")
	}
	return w.Move(sessionID, bucket)
}


func (w *WorkerService) ReadNote(workerID string) (string, int, error) {
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return "", 0, err
	}
	if rec == nil {
		return "", 0, fmt.Errorf("worker 不存在: %s", workerID)
	}
	return rec.Note, rec.NoteVer, nil
}

func (w *WorkerService) WriteNote(workerID, content string) (int, error) {
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return 0, err
	}
	if rec == nil {
		return 0, fmt.Errorf("worker 不存在: %s", workerID)
	}
	content = strings.TrimSpace(content)
	if len([]rune(content)) > MaxNoteChars {
		return 0, fmt.Errorf("公约太长(当前 %d 字,最多 %d 字):它会发给这个 worker 的每个会话,只留结论和约定",
			len([]rune(content)), MaxNoteChars)
	}
	ver := rec.NoteVer + 1
	if err := w.store.UpdateWorkerNote(workerID, content, ver); err != nil {
		return 0, err
	}
	slog.Info("worker convention updated", "worker", workerID, "ver", ver, "chars", len([]rune(content)))
	return ver, nil
}


const (
	workerNoticeJoin = "__MC_WORKER_JOIN__"
	workerNoticeNote = "__MC_WORKER_NOTE__"
	workerNoticeRole = "user"
)

func noteMarker(workerID string, ver int) string {
	return fmt.Sprintf("%s%s:%d", workerNoticeNote, workerID, ver)
}

func BuildWorkerNotice(text, marker string) provider.Message {
	return provider.Message{Role: workerNoticeRole, Content: text + "\n\n" + marker}
}

func IsWorkerNotice(m provider.Message) bool {
	if m.Role != workerNoticeRole {
		return false
	}
	t := m.GetText()
	return strings.Contains(t, workerNoticeJoin) || strings.Contains(t, workerNoticeNote)
}

func heldNoteVersion(messages []provider.Message, workerID string) int {
	want := workerNoticeNote + workerID + ":"
	best := 0
	for _, m := range messages {
		t := m.GetText()
		i := strings.Index(t, want)
		if i < 0 {
			continue
		}
		n := 0
		for _, r := range t[i+len(want):] {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		if n > best {
			best = n
		}
	}
	return best
}

func (s *Session) WorkerNotice(messages []provider.Message) provider.Message {
	s.mu.Lock()
	id := s.record.WorkerID
	name := metadataString(s.record, workerNameMetaKey)
	isRoot := s.record.ParentID == ""
	s.mu.Unlock()

	if !isRoot || s.manager == nil {
		return provider.Message{}
	}
	w := s.manager.Workers()
	if w == nil {
		return provider.Message{}
	}
	if id == WorkerUnassignedID {
		return provider.Message{}
	}
	rec, err := w.store.GetWorker(id)
	if err != nil || rec == nil || rec.NoteVer < 1 || strings.TrimSpace(rec.Note) == "" {
		return provider.Message{}
	}
	if heldNoteVersion(messages, id) >= rec.NoteVer {
		return provider.Message{}
	}
	text := fmt.Sprintf("<worker_notice>worker「%s」的公约已更新到 v%d,最新全文如下,按它执行:\n<convention>\n%s\n</convention>\n这条消息是应用注入的背景,不是用户的请求,不要回答它。</worker_notice>",
		util.FirstNonEmpty(name, rec.Name), rec.NoteVer, strings.TrimRight(rec.Note, "\n"))
	return BuildWorkerNotice(text, noteMarker(id, rec.NoteVer))
}


type WorkerToolAPI struct {
	svc       *WorkerService
	sessionID string
}

func (w *WorkerService) ToolAPIFor(sessionID string) *WorkerToolAPI {
	return &WorkerToolAPI{svc: w, sessionID: sessionID}
}

func (a *WorkerToolAPI) own() (*Session, bool) {
	sess, err := a.svc.mgr.Get(a.sessionID)
	if err != nil || sess == nil {
		return nil, false
	}
	return sess, true
}

func (a *WorkerToolAPI) ownWorker() *WorkerRecord {
	sess, ok := a.own()
	if !ok {
		return nil
	}
	sess.mu.Lock()
	id := sess.record.WorkerID
	sess.mu.Unlock()
	if id == "" {
		return nil
	}
	rec, err := a.svc.store.GetWorker(id)
	if err != nil {
		return nil
	}
	return rec
}

func (a *WorkerToolAPI) WorkerInfo() string {
	sess, ok := a.own()
	if !ok {
		return "会话不存在,无法确认 worker 归属。"
	}
	sess.mu.Lock()
	id, dir := sess.record.WorkerID, sess.record.WorkDir
	name := metadataString(sess.record, workerNameMetaKey)
	sess.mu.Unlock()
	if id == "" {
		return fmt.Sprintf("本会话尚未归属任何 worker。用 action=list 看看已有的 worker(名额 %d/%d),或 action=create 新建一个,或保持现状(一次性小问题不需要 worker)。",
			a.svc.workerCount(), a.svc.maxLimit())
	}
	rec, _ := a.svc.store.GetWorker(id)
	var b strings.Builder
	fmt.Fprintf(&b, "本会话属于 worker「%s」(id %s)。\n", util.FirstNonEmpty(name, rec2Name(rec)), id)
	workerDir := dir
	if rec != nil {
		workerDir = rec.WorkDir
	}
	fmt.Fprintf(&b, "工作目录:%s\n", dirLabel(workerDir, dir))
	if rec != nil {
		fmt.Fprintf(&b, "公约版本:v%d\n", rec.NoteVer)
		if rec.Summary != "" {
			fmt.Fprintf(&b, "摘要:%s\n", rec.Summary)
		}
		fmt.Fprintf(&b, "同 worker 会话数:%d(用 history 看它们在做什么)\n", rec.Sessions)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (w *WorkerService) workerCount() int {
	n, err := w.store.countWorkers()
	if err != nil {
		return 0
	}
	return n
}

func quotaLine(used, limit int) string {
	if left := limit - used; left > 0 {
		return fmt.Sprintf("worker 名额:还剩 %d/%d。", left, limit)
	}
	return fmt.Sprintf("worker 名额已满(%d/%d),再建要先删一个。", used, limit)
}

func (a *WorkerToolAPI) List() string {
	recs, err := a.svc.Joinable()
	if err != nil {
		return "读取 worker 列表失败: " + err.Error()
	}
	mine := ""
	if w := a.ownWorker(); w != nil {
		mine = w.WorkerID
	}
	if len(recs) == 0 {
		return fmt.Sprintf("还没有任何 worker(名额 0/%d)。如果这次工作需要一个长期共享目录(会持续多轮、多个会话),用 action=create 新建;一次性小问题可以不用 worker。", a.svc.maxLimit())
	}
	limit := a.svc.maxLimit()
	var b strings.Builder
	fmt.Fprintf(&b, "worker 名额 %d/%d(★=本会话当前所在;固定的「%s」也占一格):\n",
		len(recs), limit, WorkerOtherName)
	for _, rec := range recs {
		mark := "-"
		if rec.WorkerID == mine {
			mark = "★"
		}
		fmt.Fprintf(&b, "%s [%s] %s — %d 个会话\n", mark, rec.WorkerID, rec.Name, rec.Sessions)
		if rec.Summary != "" {
			fmt.Fprintf(&b, "    摘要:%s\n", rec.Summary)
		}
		fmt.Fprintf(&b, "    工作目录:%s\n", dirLabel(rec.WorkDir, ""))
	}
	if len(recs) >= limit {
		b.WriteString("join 用 worker_id。名额已满,create 会被拒——先删一个或加入已有的。")
	} else {
		b.WriteString("join 用 worker_id;新建用 create。")
	}
	return b.String()
}

func (a *WorkerToolAPI) Create(name, summary, copyFrom string) string {
	base := ""
	if sess, ok := a.own(); ok {
		sess.mu.Lock()
		base = sess.record.WorkDir
		sess.mu.Unlock()
	}
	rec, err := a.svc.Create(name, summary, base, copyFrom)
	if err != nil {
		return "创建 worker 失败: " + err.Error()
	}
	if _, err := a.svc.Join(a.sessionID, rec.WorkerID); err != nil {
		return fmt.Sprintf("worker「%s」已建好(id %s,目录 %s),但本会话加入失败:%s", rec.Name, rec.WorkerID, rec.WorkDir, err)
	}
	return fmt.Sprintf(
		"已创建并加入 worker「%s」(id %s)。\n共享工作目录:%s\n"+
			"本回合已建的工具仍指向旧目录,后续文件操作请用绝对路径;下一个任务起工作目录自动切到上面这个。\n"+
			"%s\n%s",
		rec.Name, rec.WorkerID, rec.WorkDir,
		quotaLine(a.svc.workerCount(), a.svc.maxLimit()), conventionBlock(rec))
}

func (a *WorkerToolAPI) Join(workerID string) string {
	rec, err := a.svc.Join(a.sessionID, workerID)
	if err != nil {
		return "加入 worker 失败: " + err.Error()
	}
	hint := "用 history 看同 worker 的其他会话在做什么。\n" + conventionBlock(rec)
	if strings.TrimSpace(rec.WorkDir) == "" {
		return fmt.Sprintf(
			"已加入 worker「%s」(id %s)。这个 worker 不设共享目录,本会话继续用自己的目录:%s\n%s",
			rec.Name, rec.WorkerID, a.workDirOf(), hint)
	}
	return fmt.Sprintf(
		"已加入 worker「%s」(id %s)。\n共享工作目录:%s\n"+
			"本回合已建的工具仍指向旧目录,后续文件操作请用绝对路径;下一个任务起工作目录自动切到上面这个。\n"+
			"%s",
		rec.Name, rec.WorkerID, rec.WorkDir, hint)
}

func dirLabel(workerDir, ownDir string) string {
	if strings.TrimSpace(workerDir) != "" {
		return workerDir
	}
	if ownDir != "" {
		return ownDir + "(该 worker 不设共享目录,上面是本会话自己的目录)"
	}
	return "(不设共享目录,会话保持自己的目录)"
}

func conventionBlock(rec *WorkerRecord) string {
	if rec == nil {
		return ""
	}
	if rec.NoteVer < 1 || strings.TrimSpace(rec.Note) == "" {
		return "公约:还没有(公约只能由用户在界面里写,你只能 note_read)。"
	}
	return fmt.Sprintf("公约 v%d,按它执行:\n<convention>\n%s\n</convention>\n%s",
		rec.NoteVer, strings.TrimRight(rec.Note, "\n"), noteMarker(rec.WorkerID, rec.NoteVer))
}

func (a *WorkerToolAPI) Leave() string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话本来就没有归属 worker。"
	}
	if err := a.svc.Leave(a.sessionID); err != nil {
		return "退出 worker 失败: " + err.Error()
	}
	return fmt.Sprintf("已退出 worker「%s」。工作目录保持在 %s 不变(如需换目录请说明)。下一个任务起不再注入该 worker 的目录与公约。",
		rec.Name, a.workDirOf())
}

func (a *WorkerToolAPI) Update(summary string) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,改写不了摘要。"
	}
	if err := a.svc.UpdateSummary(rec.WorkerID, summary); err != nil {
		return "改写摘要失败: " + err.Error()
	}
	return "已更新 worker「" + rec.Name + "」的摘要,别的会话下次 list 就能看到。"
}

func (a *WorkerToolAPI) History(limit int) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker。"
	}
	sums, err := a.svc.store.ListBucketSummaries(rec.WorkerID, limit, 0)
	if err != nil {
		return "读取兄弟会话失败: " + err.Error()
	}
	var b strings.Builder
	n := 0
	for _, sum := range sums {
		if sum.SessionID == a.sessionID {
			continue
		}
		n++
		fmt.Fprintf(&b, "- [%s] %s — %s,%d 条,%s\n", sum.SessionID, util.FirstNonEmpty(sum.Title, "未命名会话"),
			sum.Status, sum.MessageCount, relTimeShort(sum.UpdatedAt))
		if sum.WorkDir != "" && sum.WorkDir != rec.WorkDir {
			fmt.Fprintf(&b, "    (工作目录:%s)\n", sum.WorkDir)
		}
	}
	if n == 0 {
		return "同 worker 目前只有本会话,没有别的会话。"
	}
	return fmt.Sprintf("worker「%s」的其他会话(共 %d 个,按更新时间):\n%s用 messages(session_id) 读某个会话的具体内容。",
		rec.Name, n, b.String())
}

func (a *WorkerToolAPI) Messages(sessionID string, chars int) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,不能读别的会话。"
	}
	other, err := a.svc.store.Get(sessionID)
	if err != nil || other == nil {
		return "找不到会话: " + sessionID
	}
	if other.WorkerID != rec.WorkerID {
		return "只能读取同一个 worker 里其他会话的历史。"
	}
	msgs := a.svc.mgr.CopyMessages(sessionID)
	if len(msgs) == 0 {
		return "会话「" + util.FirstNonEmpty(other.Title, sessionID) + "」还没有消息。"
	}
	return renderConversation(util.FirstNonEmpty(other.Title, sessionID), msgs, chars)
}

func (a *WorkerToolAPI) NoteRead() string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,没有公约可读。"
	}
	return fmt.Sprintf("公约 v%d\n%s", rec.NoteVer, util.FirstNonEmpty(strings.TrimSpace(rec.Note), "(空的)"))
}

func (a *WorkerToolAPI) workDirOf() string {
	sess, ok := a.own()
	if !ok {
		return ""
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.record.WorkDir
}

func renderConversation(title string, msgs []provider.Message, chars int) string {
	type item struct{ role, text string }
	var items []item
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		if len([]rune(text)) > 800 {
			text = string([]rune(text)[:800]) + "…"
		}
		items = append(items, item{role: m.Role, text: text})
	}
	if len(items) == 0 {
		return "会话「" + title + "」没有可读的文本消息。"
	}
	used := 0
	start := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		cost := len([]rune(items[i].text)) + 12
		if used+cost > chars {
			break
		}
		used += cost
		start = i
	}
	var b strings.Builder
	fmt.Fprintf(&b, "会话「%s」最近的对话(从第 %d/%d 条起,共 %d 条消息):\n", title, start+1, len(items), len(msgs))
	for _, it := range items[start:] {
		fmt.Fprintf(&b, "[%s] %s\n", it.role, it.text)
	}
	return strings.TrimRight(b.String(), "\n")
}


func workerSlug(name string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(name) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_'
		if ok {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if slug == "" {
		return util.RandomHex(4)
	}
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return strings.Trim(slug, "-.")
}

func uniqueWorkerDir(base, name string) string {
	p := filepath.Join(base, name)
	for i := 2; pathExists(p); i++ {
		p = filepath.Join(base, fmt.Sprintf("%s-%d", name, i))
	}
	return p
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func seedFrom(dir, copyFrom string) (string, error) {
	src, err := filepath.Abs(strings.TrimSpace(copyFrom))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("copy_from 不存在: %s", src)
	}
	if src == filepath.Clean(dir) || strings.HasPrefix(strings.ToLower(src+string(os.PathSeparator)), strings.ToLower(dir+string(os.PathSeparator))) {
		return "", fmt.Errorf("copy_from 不能是新工作目录本身或它里面的东西: %s", src)
	}
	if info.IsDir() {
		if err := tools.CopyTree(src, dir); err != nil {
			return "", err
		}
		return src, nil
	}
	return src, tools.CopyTree(src, filepath.Join(dir, filepath.Base(src)))
}

func readWorkerNote(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}


func rec2Name(rec *WorkerRecord) string {
	if rec == nil {
		return ""
	}
	return rec.Name
}

func relTimeShort(ts string) string {
	if len(ts) >= 16 {
		return ts[:16]
	}
	return ts
}
