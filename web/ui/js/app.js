/* ---------------- 初始化 ---------------- */
(async function init() {
  // “＋ 新建会话”:高频操作一键直达(不再弹下拉)。归属跟着当前选中的区走
  // (创建即归属,大按钮和小「＋ 会话」同一语义)。
  $("#btn-new").addEventListener("click", () => {
    if (state.currentGroup) exitGroupView();
    detachWorkerForNewSession();
    resetToNew(); $("#input").focus();
  });
  // “新建群聊”:群区标题旁的就地入口
  $("#btn-new-group").addEventListener("click", (e) => {
    e.stopPropagation();
    openCreateGroupModal();
  });
  // 群区折叠/展开(记忆状态;标题点击仅用于从折叠态展开)
  let collapsed = false;
  try { collapsed = localStorage.getItem("mc-group-collapsed") === "1"; } catch (e) {}
  setGroupCollapsed(collapsed);
  $("#group-toggle").addEventListener("click", (e) => {
    e.stopPropagation();
    setGroupCollapsed(!$("#group-sec").classList.contains("collapsed"));
  });
  $("#group-head-title").addEventListener("click", () => {
    if ($("#group-sec").classList.contains("collapsed")) setGroupCollapsed(false);
  });
  // 群视图头部按钮
  $("#btn-group-members").addEventListener("click", toggleGroupPanel);
  $("#btn-group-settings").addEventListener("click", () => {
    if (state.currentGroup) openGroupSettings(state.currentGroup);
  });
  document.addEventListener("click", (e) => {
    const p = $("#group-panel");
    if (p.classList.contains("open") && !p.contains(e.target) && e.target.id !== "btn-group-members") closeGroupPanel();
  });
  $("#btn-send").addEventListener("click", send);
  $("#btn-stop").addEventListener("click", stop);
  // 历史懒加载:上滑接近消息区顶部(120px 内)自动按批补齐更早消息
  messagesEl().addEventListener("scroll", () => {
    if (messagesEl().scrollTop < 120 && histStart > 0) loadOlder();
  });
  $("#input").addEventListener("keydown", (e) => {
    if (fsState.open) {
      if (e.key === "ArrowDown") { e.preventDefault(); fsMove(1); return; }
      if (e.key === "ArrowUp") { e.preventDefault(); fsMove(-1); return; }
      if (e.key === "Escape") { e.preventDefault(); fsClose(); return; }
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        if (fsState.hits.length) fsInsert(fsState.sel);
        else { fsClose(); send(); } // 无命中:直接按原逻辑发送
        return;
      }
    }
    // 发送键随回车模式(点底部提示切换): 默认 Enter 发送; 反向模式 Shift+Enter 发送, Enter 换行
    const wantSend = sendMode === "shift" ? (e.key === "Enter" && e.shiftKey)
                                          : (e.key === "Enter" && !e.shiftKey);
    if (wantSend) { e.preventDefault(); send(); }
  });
  $("#foot-hint").addEventListener("click", toggleSendMode);
  $("#input").addEventListener("input", () => { autoGrow(); fsCheckInput(); });
  $("#input").addEventListener("blur", fsClose);
  // 粘贴截图:输入框里 Ctrl+V 直接取剪贴板里的图片
  $("#input").addEventListener("paste", (e) => {
    const items = e.clipboardData && e.clipboardData.items;
    if (!items) return;
    const files = [];
    for (const it of items) {
      if (it.type && it.type.startsWith("image/")) {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (files.length) { e.preventDefault(); addImageFiles(files); }
  });
  $("#btn-plus").addEventListener("click", () => { $("#file-shot").click(); });
  $("#file-shot").addEventListener("change", (e) => {
    addImageFiles(Array.from(e.target.files || []));
    e.target.value = "";
  });
  $("#btn-compact").addEventListener("click", async () => {
    if (!state.current || state.running) return;
    const b = $("#btn-compact");
    b._busy = true;
    b.disabled = true;
    b.textContent = "压缩中…";
    try {
      const r = await api("/api/sessions/" + state.current + "/compact", { method: "POST" });
      if (r.compressed) {
        toast("上下文已压缩:" + r.before.toLocaleString() + " → " + r.after.toLocaleString() +
          " tokens(释放 " + (r.before - r.after).toLocaleString() + ")", "ok");
      } else {
        toast("无需压缩:对话太短或没有可压缩的部分", "");
      }
      updateTokenUsage();
    } catch (e) {
      toast("压缩失败:" + e.message, "err");
    } finally {
      b._busy = false;
      b.textContent = "⚡ 压缩";
      compactBtnState();
    }
  });
  $("#btn-settings").addEventListener("click", openSettings);
  initSettingsNav();
  initBootSettings();
  initPluginSettings();
  $("#c-theme").addEventListener("change", (e) => {
    const m = e.target.value;
    setThemeMode(m);
    toast(m === "light" ? "已切换为浅色主题" : m === "dark" ? "已切换为深色主题" : "已切换为跟随系统主题", "ok");
  });
  $("#btn-user-ava").addEventListener("click", openUserAvatarModal);
  $("#btn-cancel").addEventListener("click", () => $("#settings-mask").classList.remove("open"));
  $("#btn-save").addEventListener("click", saveSettings);
  $("#settings-mask").addEventListener("click", (e) => {
    if (e.target.id === "settings-mask") e.target.classList.remove("open");
  });
  // 权限模式 / 上下文提醒 / 子任务 / 审批
  $("#btn-mode").addEventListener("click", toggleMode);
  $("#ctx-reminders").addEventListener("change", toggleContextReminders);
  // 模型按钮(composer 输入行内):单击切换模型,双击调整思考模式(参考 codex)。
  // 用定时器区分单击/双击:双击在 230ms 内到齐则取消单击动作。
  let modelClickTimer = null;
  $("#btn-model").addEventListener("click", () => {
    if (modelClickTimer) {
      clearTimeout(modelClickTimer);
      modelClickTimer = null;
      openThinkingPop();
      return;
    }
    modelClickTimer = setTimeout(() => {
      modelClickTimer = null;
      toggleModelMenu();
    }, 230);
  });
  // 思考强度滑块:拖动实时预览,松手(change)才提交。
  $("#tp-effort").addEventListener("input", () => {
    const scale = thinkScale();
    if (!scale) return;
    let idx = parseInt($("#tp-effort").value, 10);
    if (isNaN(idx) || idx < 0) idx = 0;
    if (idx >= scale.length) idx = scale.length - 1;
    state.thinking = scale[idx].thinking;
    state.reasoningEffort = scale[idx].effort;
    paintReasoning();
    updateThinkFill();
  });
  $("#tp-effort").addEventListener("change", () => persistReasoning());
  // 设置弹窗:新增模型 = 追加一块并直接进入该块的编辑面板
  $("#btn-add-model").addEventListener("click", () => {
    modelDraft.push(modelTemplate());
    modelEditing = modelDraft.length - 1;
    renderModels();
    const f = $("#model-edit").querySelector('input[data-k="name"]');
    if (f) {
      f.focus();
      const host = $("#model-edit").querySelector(".model-editor");
      if (host) host.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
  });
  // 双击聊天头标题重命名当前会话(群视图:打开群设置)
  $("#chat-title").addEventListener("dblclick", () => {
    if (state.currentGroup) { openGroupSettings(state.currentGroup); return; }
    if (!state.current) return;
    const s = state.sessions.find(x => x.session_id === state.current);
    renameSession(state.current, s ? s.title : "");
  });
  $("#btn-tasks").addEventListener("click", toggleTasksPanel);
  document.addEventListener("click", (e) => {
    const p = $("#tasks-panel");
    if (p.classList.contains("open") && !p.contains(e.target) && e.target.id !== "btn-tasks") closeTasksPanel();
    // 点模型按钮/菜单之外时收起模型下拉和思考浮层
    const wrap = $("#model-menu").closest(".model-menu-wrap");
    if (wrap && !wrap.contains(e.target)) {
      if ($("#model-menu").classList.contains("open")) closeModelMenu();
      if ($("#think-pop").classList.contains("open")) closeThinkingPop();
    }
  });
  $("#btn-approval-badge").addEventListener("click", () => {
    const ids = Object.keys(state.approvals);
    if (ids.length) showApproval(ids[0]);
  });
  $("#ap-approve").addEventListener("click", () => { if (state.approvalShown) decideApproval(state.approvalShown, "approve"); });
  $("#ap-deny").addEventListener("click", () => { if (state.approvalShown) decideApproval(state.approvalShown, "deny"); });
  $("#approval-mask").addEventListener("click", (e) => {
    if (e.target.id === "approval-mask") hideApproval(); // 只是收起视图,请求仍待审批
  });

  // worker(工作大类)切换条要在拉列表之前恢复:loadSessions 按 state.worker 分桶,
  // 先跑的话刷新后拉到的永远是"未分配"桶——侧栏显示上次的 worker、列表却是
  // 另一个桶的空态,当前会话也不在列表里(实测:刷新后当前会话整个没了)。
  initWorkerBar();
  await loadSessions();
  if (GROUP_CHAT_ENABLED) {
    loadGroups();
  } else {
    // 群聊隐藏:其侧边栏位置由上方的 worker 切换条承接(群区元素保留但不可见)
    const gsec = $("#group-sec");
    if (gsec) gsec.style.display = "none";
  }
  loadStatus();
  connectEvents();
  paintMode();
  paintContextReminders();
  syncApprovals(); // 页面刷新后同步未决审批
  await loadUserAvatar();  // 用户头像:先于会话恢复,历史消息直接带头像渲染
  await loadAgentAvatar(); // agent 头像同理(没设置就是默认那颗墨色 M)
  // 刷新页面后恢复之前打开的会话(含其执行状态)。
  // 不在"当前桶的列表"里找它:会话所属 worker 和恢复出来的分桶未必一致,
  // 旧检查把"列表里没有"当成"已删除",记录直接被扔掉——刷新后当前会话没了。
  // 直接按 id 打开:selectSession 自会按 id 拉取,followSessionWorker 把侧栏
  // 切到它所属的分桶;真拉不到(已删除)才丢弃记录。
  const saved = readSavedSession();
  if (saved) {
    try {
      await api("/api/sessions/" + saved);
      selectSession(saved);
    } catch (e) { saveSession(null); }
  }
  setInterval(loadStatus, 30000);
  // 定期轮询:校准会话列表,并把当前会话的执行状态同步回服务端
  // (运行中也不跳过,这样 SSE 漏掉终结事件时停止按钮能自动收敛)
  setInterval(() => {
    loadSessions();
    loadWorkers();
    if (GROUP_CHAT_ENABLED) loadGroups();
    updateTokenUsage();
    syncApprovals();
  }, 30000);
  $("#input").focus();
  // 第三类插件:把 slot 摆到位、把宠物画出来、把动效绑上。
  // 放在最后是因为它要读已经渲染好的 DOM 锚点;没装插件时整体是空操作。
  if (typeof pluginRuntimeInit === "function") pluginRuntimeInit();
})();
