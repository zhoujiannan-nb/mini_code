 /* ---------------- SSE 事件 ---------------- */
 // 新会话发送后的"接管"动作:把当前视图切到刚创建的会话。
 // session_created 无论先到还是后到,按 task_id 确认后统一走这里。
  function adoptNewSession(sid) {
    state.current = sid;  // 本地视图已包含用户消息,不重新拉历史
    saveSession(sid);
    setChatTitle("会话 " + sid.slice(0, 8), "");
    renderSessions();
    paintTokens(0);
    paintCacheHit(0, 0, false); // 新会话:统计从零开始
    speedStopTicker();
    paintSpeed(0, false);
    compactBtnState();
   paintMode();
    paintModel(); // 会话已创建:按钮提示从"新会话"切到"当前会话模型"
    paintBackBtn("");
    loadChildren();
    // hub 在 session_created 之后登记任务,登记完成会推 task_started 事件;
    // onEvent 的 status 分支收到后立刻 loadSessions,侧边栏"执行中"随即
    // 点亮——不再靠猜 1s 延迟(那段时间里 active 还没置位,立即拉会误闪)。
  }
 function onEvent(m) {
  // 审批事件跨会话(子代理/其他会话的危险操作也要用户拍板),先于会话过滤处理
  if (m.kind === "approval_request") {
    try { onApprovalRequest(JSON.parse(m.content)); } catch (e) {}
    return;
  }
  if (m.kind === "approval_resolved") {
    try { onApprovalResolved(JSON.parse(m.content)); } catch (e) {}
    return;
  }
   if (m.kind === "status" && m.status === "session_created") {
    if (!state.sessions.some(s => s.session_id === m.session_id)) {
      state.sessions.unshift({ session_id: m.session_id, title: "新会话", status: "created", message_count: 0 });
    }
    if (m.task_id) {
      // 记下最近的建会话事件:偶发竞态下(后端先 emit SSE 再回 HTTP)
      // 本条可能比 POST /api/messages 的响应早到,响应回来按 task_id 精确匹配接管。
      state.recentCreated.push({ task_id: m.task_id, session_id: m.session_id });
      if (state.recentCreated.length > 8) state.recentCreated.shift();
    }
    if (m.task_id && state.pendingTask && m.task_id === state.pendingTask) {
      state.pendingTask = null;
      adoptNewSession(m.session_id);
    } else {
      // 其他渠道新建的会话,或本端事件早到还没响应:
      // 侧边栏立即可见,随后补齐真实状态
      renderSessions();
      scheduleListRefresh(1000);
    }
    return;
   }
  // 严格按会话隔离:其他会话的事件(包括当前停在新会话视图时)一律不写入
  // 当前视图、不影响 running 状态;只当作列表变化信号,防抖刷新侧边栏。
  if (m.session_id && m.session_id !== state.current) {
    scheduleListRefresh();
    return;
  }
  // 到达这里的会话事件都属于当前会话(上方已按 session_id 严格过滤)
  switch (m.kind) {
    case "reasoning_delta":
      liveStart();
      liveUpdate("reasoning", m.reasoning || m.content);
      break;
    case "content_delta":
      liveStart();
      liveUpdate("content", m.content);
      speedDelta(m.content); // 实时估算输出速度(滚动窗口)
      break;
    case "tool_start":
      pendingRemove();
      liveCommit();
      addToolLine(m.tool_name, m.tool_args);
      break;
    case "tool_args":
      // 模型还在生成参数的工具:先立一张"预备步骤",把正在写的字符显示出来
      appendToolArgs(m.tool_name, m.content);
      break;
    case "tool_output":
      // 运行中工具的实时输出:只贴到「正在执行」卡里那一步,不落库、不进历史
      appendToolOutput(m.tool_name, m.content);
      break;
    case "tool_end":
      markToolPendingDone(m.tool_result, m.tool_name);
      // workers 工具会改这条会话自己的归属(join/create/leave):跑完立刻把侧栏
      // 对齐到新的 worker,不然新会话加入了 worker 还停在「未分配」。
      if (m.tool_name === "workers" && typeof resyncSessionWorker === "function") resyncSessionWorker(state.current);
      break;
    case "context_usage":
      // 每轮 loop 开头(压缩检查后)后端推送的上下文 token 数,直接画,不走 HTTP
      paintTokens(m.tokens);
      break;
    case "usage":
      // 每轮 LLM 调用后后端推送的真实 usage,画缓存命中率与实测输出速度
      paintCacheHit(m.prompt_tokens, m.cached_tokens, m.cache_reported);
      rememberCacheHit(state.current, m.prompt_tokens, m.cached_tokens, m.cache_reported);
      if (m.speed > 0) { paintSpeed(m.speed, true); speedStopTicker(); } // 实测值覆盖实时估算
      break;
    case "assistant":
      pendingRemove();
      liveRemove();
      speedStopTicker(); // 任务结束:保留最后一次的展示值
      if (m.content || m.reasoning) addAssistant(m.content, m.reasoning);
      stepsClose(); // 收尾:没被并进去的过程步骤独立成卡
      stepsRunFreeze(); // 在跑的工具步骤不再计时
      setRunning(false);
      updateTokenUsage();
      loadSessions();
      break;
    case "error":
      pendingRemove();
      liveRemove();
      speedStopTicker();
      stepsClose();
      stepsRunFreeze();
      addError(m.content);
      clearSending(); // 任务没起来:结束 sending 保护期,允许降级
      setRunning(false);
      updateTokenUsage();
      loadSessions();
      break;
    case "status":
      if (m.status === "task_started") {
        // 服务端已把任务登记进活动表(ActiveTask=true):立即拉列表,
        // 侧边栏"执行中"即时点亮,不再等 30s 轮询;同时结束 sending 保护期。
        clearSending();
        loadSessions();
        break;
      }
      if (m.status === "interrupted") {
        pendingRemove();
        liveRemove();
        speedStopTicker();
        stepsRunFreeze();
        addStatus("⏹ 任务已中断(已保留进度,可继续发送消息)");
        setRunning(false);
      } else if (m.status === "task_done") {
        pendingRemove();
        liveRemove();
        speedStopTicker();
        stepsRunFreeze();
        setRunning(false);
      }
      updateTokenUsage();
      loadSessions();
      break;
  }
}
let esRef = null;           // 当前 EventSource(看门狗重连时先关旧连接)
let sseAliveAt = 0;         // 最近一次收到服务端数据/心跳的时间
let sseWatchdog = null;

/* ---------------- 断线对账(resync) ----------------
   SSE 是发后即忘:tool_end 这类一次性事件在断流窗口里丢了就永远不回,
   「正在执行」卡会挂在界面里继续计时,后续输出全排在它下面——而库里的
   真相早就带着结果了(实测:一条 sleep 540 的 exec 断流后卡永远"运行中")。
   对账读的是会话事件账本(session_event):append-only 的事实流水,
   GET /api/sessions/:id/events?tail_run=1 直接给出「本轮任务」的全部事件
   ——正是断流窗口可能藏掉的那一段:
   - tool/result 事件按工具名配给「待返回/运行中」的步骤(每份结果只配一次);
   - 配不上的不动它——它可能真的还在跑,结果会随下一批事件回来;
   - 任务其实已结束(active=false)→ 剩余步骤停表收条。
   只填已发生的、不发明未发生的:对账本身幂等,多跑一次只是多一次 GET。 */
async function resyncRunning() {
  if (!state.current || !state.running) return;
  const sid = state.current;
  let data;
  try { data = await api("/api/sessions/" + sid + "/events?tail_run=1"); } catch (e) { return; }
  if (state.current !== sid) return; // 等待期间已切走
  const evs = data.events || [];
  const done = []; // 本轮已发生的 tool 结果:同名并行调用各拿各的,不抢同一份
  for (const ev of evs) {
    if (ev.type !== "tool/result" || !ev.payload) continue;
    done.push({
      name: String(ev.payload.name || "tool"),
      text: String(ev.payload.result != null ? ev.payload.result : (ev.payload.error || "")),
    });
  }
  const used = {};
  for (const st of [].slice.call(messagesEl().querySelectorAll('.ms-step[data-soft="1"]'))) {
    const name = st.dataset.name || "tool";
    let filled = false;
    for (let i = 0; i < done.length; i++) {
      if (used[i] || done[i].name !== name) continue;
      used[i] = true;
      stepsFill(st, done[i].text);
      filled = true;
      break;
    }
    // 结果还没回来、而任务确实还在跑:把静态的"待返回"升级成转圈等待。
    if (!filled && data.active) stepsTag(st, "soft", "待返回");
  }
  if (!data.active) stepsRunFreeze();
}

function connectEvents() {
  if (esRef) { try { esRef.close(); } catch (e) {} esRef = null; }
  const es = esRef = new EventSource("/api/events");
  // 每次连接建立(首连/浏览器自动重连/看门狗重连)都对账一次。
  // 首连时 state.running 还是 false,resyncRunning 自己会退场,不多打接口。
  es.onopen = () => { resyncRunning(); };
  sseAliveAt = Date.now();
  es.onmessage = (e) => {
    sseAliveAt = Date.now();
    try { onEvent(JSON.parse(e.data)); } catch (err) {}
  };
  // 服务端每 30s 发一个 event:ping 心跳(真实 SSE 事件,不是注释):
  // 即使 TCP 半开(收不到任何数据),看门狗也能在 75s 内发现并强制重连
  es.addEventListener("ping", () => { sseAliveAt = Date.now(); });
  es.onerror = () => { /* EventSource 自动重连 */ };
  if (!sseWatchdog) {
    sseWatchdog = setInterval(() => {
      if (Date.now() - sseAliveAt > 75000) {
        toast("事件流无响应,已自动重连", "");
        connectEvents();
      }
    }, 10000);
  }
}

