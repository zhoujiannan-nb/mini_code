package session

import (
	"time"

	"github.com/user/mini_code/provider"
)

/*
---- 模型"正在打字"的参数也推给界面 ----

	write_file 的 content 是模型一个字一个字生成的:一部小说的大纲能有几十 KB,要跑一两分钟。
	这段时间参数只是流式增量,tool_start 得等整段 assistant 收完才发,所以界面完全不知道它准备
	写哪个文件、已经写了多少 —— 只有 token 数在涨。这里把参数的增长按 exec 那套节奏(攒够
	argsFeedBytes 或距上次超过 argsFeedWait)推出去,前端就能边生成边显示。

	推的是**已生成的全部参数**(不是增量),和 StreamChunk.Content/ReasoningContent 的约定一致:
	- 前端每次重算要显示的正文,天然幂等,丢一个包无所谓;
	- chatWithRetry 失败重试会从头重生成,增量语义会把上一次的残留接在后面,快照语义直接覆盖掉。
*/
const (
	argsFeedBytes = 1024                   // 比上次多这么多就推一次
	argsFeedWait  = 250 * time.Millisecond // 没攒够也最多等这么久,长回答不至于卡着不动
)

type argsFeedCall struct {
	name string
	// sentArgs is what the UI was last given. Comparing the text (not just its
	// length) is what lets a retry - which restarts the arguments from scratch and
	// can easily land on the same length - still be reported instead of leaving
	// an abandoned draft on screen.
	sentArgs string
	last     time.Time
}

// argsStreamer forwards argument growth for one LLM call. A nil *argsStreamer is
// inert, which is how the "nobody is listening" case stays free.
type argsStreamer struct {
	emit  func(name, argsSoFar string)
	calls map[int]*argsFeedCall
}

func newArgsStreamer(emit func(name, argsSoFar string)) *argsStreamer {
	if emit == nil {
		return nil
	}
	return &argsStreamer{emit: emit, calls: map[int]*argsFeedCall{}}
}

// push is called with the provider's per-chunk snapshot of in-flight tool calls.
func (a *argsStreamer) push(calls []provider.StreamToolCallProgress) {
	if a == nil || len(calls) == 0 {
		return
	}
	now := time.Now()
	for _, c := range calls {
		st := a.calls[c.Index]
		if st == nil {
			st = &argsFeedCall{}
			a.calls[c.Index] = st
		}
		if st.name == "" {
			st.name = c.Name // 名字只在这个调用的第一个增量里出现,认第一次拿到的
		}
		if st.name == "" || len(c.Args) == 0 || c.Args == st.sentArgs {
			continue
		}
		// grew<0 means the arguments restarted (chatWithRetry re-sends from
		// scratch): report at once, otherwise the UI would keep showing the tail
		// of an abandoned draft after the snapshot it renders from got shorter.
		if grew := len(c.Args) - len(st.sentArgs); grew > 0 && grew < argsFeedBytes && !st.last.IsZero() && now.Sub(st.last) < argsFeedWait {
			continue
		}
		a.emit(st.name, c.Args)
		st.sentArgs = c.Args
		st.last = now
	}
}
