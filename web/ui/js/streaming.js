/* ---------------- 等待首 token(转圈) ---------------- */
function pendingStart() {
  pendingRemove();
  hidePlaceholder();
  const t0 = Date.now();
  const div = document.createElement("div");
  div.className = "msg pending";
  div.innerHTML =
    '<div class="m-card">' +
    '<span class="spinner"></span>' +
    '<span>等待模型响应…(大上下文首次推理可能较慢)</span>' +
    '<span class="pending-sec"></span>' +
    "</div>";
  const stick = nearBottom();
  messagesEl().appendChild(div);
  div._timer = setInterval(() => {
    const el = div.querySelector(".pending-sec");
    if (el) el.textContent = Math.floor((Date.now() - t0) / 1000) + "s";
  }, 1000);
  state.pending = div;
  scrollBottom(false, stick);
}
function pendingRemove() {
  if (state.pending) {
    clearInterval(state.pending._timer);
    state.pending.remove();
    state.pending = null;
  }
}

/* ---------------- 流式输出块 ---------------- */
function liveStart() {
  // 自愈:state.live 指向的节点若已被移除(容器重建/切视图没清引用),
  // 按不存在处理——否则此后每个 delta 都会在这里早退,转圈永不消除、
  // 流式块不再出现(模型在输出但聊天区一直"等待模型响应")。
  if (state.live && !state.live.isConnected) state.live = null;
  if (state.live) return;
  pendingRemove(); // 收到首个 token,撤掉转圈
  hidePlaceholder();
  const m = messagesEl();
  const div = document.createElement("div");
  div.className = "msg ai";
  div.innerHTML =
    '<div class="m-card">' +
    '<div class="m-head"><span class="m-tag ai">Mini Code</span><span class="live-ind"><span class="pulse"></span>思考中…</span></div>' +
    '<div class="m-think-live"></div>' +
    '<div class="m-text" style="display:none"></div>' +
    "</div>";
  div.insertAdjacentHTML("afterbegin", agentAvatarBox("c-avatar"));
  const stick = nearBottom();
  m.appendChild(div);
  state.live = div;
  scrollBottom(false, stick);
}
  function liveUpdate(kind, text) {
    if (!state.live) return;
    const stick = nearBottom(); // 用户在底部才跟随;上滚回看时不打扰(每帧只测一次)
   if (kind === "reasoning") {
     const t = state.live.querySelector(".m-think-live");
     t._raw = text;
     t.classList.add("md");
     t.innerHTML = mdToHtml(text);
     t.style.display = "block";
     t.scrollTop = t.scrollHeight;
   } else {
     const b = state.live.querySelector(".m-card > .m-text");
     b._raw = text;
     b.classList.add("md");
     b.innerHTML = mdToHtml(text);
     b.style.display = "block";
     // 正文开始后,指示器从"思考中"切到"生成中"
     const ind = state.live.querySelector(".live-ind");
      if (ind && !b._labelled) { ind.lastChild.textContent = "生成中…"; b._labelled = true; }
    }
    scrollBottom(false, stick);
  }
function liveRemove() {
  if (state.live) { state.live.remove(); state.live = null; }
}
 // 把流式块固化为一条普通消息(用于工具调用前已产生的中间回复)
 function liveCommit() {
   if (!state.live) return;
   const think = state.live.querySelector(".m-think-live");
   const body = state.live.querySelector(".m-card > .m-text");
   const r = think._raw != null ? think._raw : (think.textContent || "");
   const c = body._raw != null ? body._raw : (body.textContent || "");
   state.live.remove();
   state.live = null;
   if (!r && !c) return;
   addAssistant(c, r);
 }

