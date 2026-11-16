// 应用骨架 + 启动流程。布局与旧 index.html 一致:
//   <aside>侧栏</aside> + body 层的 worker 下拉 + <main>顶栏/聊天区/输入区</main>
//   + 各种 modal 宿主 + #toast。
//
// #app 在 css/app.css 里是 display:contents —— 这样 <aside>/<main> 仍然直接参与
// body 的 flex 布局(换渲染引擎不换皮),worker 下拉也仍然不受 aside 的
// backdrop-filter 包含块影响。
import { h, Fragment } from "preact";
import { useEffect } from "preact/hooks";
import htm from "htm";

import { ROUTES, api } from "../lib/api.js";
import { getState, setState } from "../lib/store.js";
import {
  initWorkerBar, loadSessions, loadWorkers, loadStatus, loadChildren, loadJobs,
  updateTokenUsage, syncApprovals, loadUserAvatar, loadAgentAvatar,
  readSavedSession, saveSession, selectSession, resetToNew, detachWorkerForNewSession,
} from "../lib/actions.js";
import { connectEvents, disconnectEvents } from "../lib/sse.js";
import { bindMarkdownLinks } from "../lib/markdown.js";
import { pluginRuntimeInit } from "../lib/plugin-runtime.js";
import { Sidebar } from "./Sidebar.js";
import { WorkerMenu } from "./WorkerMenu.js";
import { Topbar, ChatHead } from "./Topbar.js";
import { Chat } from "./Chat.js";
import { Composer } from "./Composer.js";
import { ContextPanel } from "./ContextPanel.js";
import { TrajPanel } from "./TrajPanel.js";
import { SummaryPanel } from "./SummaryPanel.js";
import { Settings } from "./Settings.js";
import { ApprovalModal } from "./panels/ApprovalModal.js";
import { UserAvatarModal } from "./panels/UserAvatarModal.js";
import { WorkerNoteModal, PluginLogsModal } from "./panels/NoteModal.js";
import { Toast } from "./Toast.js";

const html = htm.bind(h);

// 启动:先把 worker 桶恢复出来再拉列表(顺序反了会把刷新前的会话"弄丢"),
// 然后接 SSE;最后按 id 打开上次的会话(不要求它出现在当前桶的列表里)。
async function bootstrap() {
  initWorkerBar();
  // worker 列表要先于会话列表:切换条的名字/计数、以及按 worker 分桶的拉取都靠它。
  await loadWorkers();
  await loadSessions();
  loadStatus();
  connectEvents();
  syncApprovals(); // 页面刷新后同步未决审批
  await loadUserAvatar();  // 用户头像:先于会话恢复,历史消息直接带头像渲染
  await loadAgentAvatar(); // agent 头像同理(没设置就是默认那颗墨色 M)
  const saved = readSavedSession();
  if (saved) {
    try {
      await api(ROUTES.session(saved));
      selectSession(saved);
    } catch (e) { saveSession(null); }
  }
}

export function App() {
  useEffect(() => {
    bindMarkdownLinks();
    pluginRuntimeInit();
    bootstrap();

    // 定期轮询:校准会话列表,并把当前会话的执行状态同步回服务端
    // (运行中也不跳过,这样 SSE 漏掉终结事件时停止按钮能自动收敛)
    const slow = setInterval(() => {
      loadStatus();
      loadSessions();
      loadWorkers();
      updateTokenUsage();
      syncApprovals();
    }, 30000);

    // 全局点击:关面板 / 关 worker 下拉 / 点头像开弹窗 / 点截图放大。
    const onDocClick = (e) => {
      const st = getState();
      const t = e.target;
      if (st.workerMenuOpen && !t.closest("#worker-menu") && !t.closest("#btn-wk-filter")) {
        setState({ workerMenuOpen: false });
      }
      if (st.tasksOpen && !t.closest("#tasks-panel") && !t.closest("#btn-tasks")) {
        setState({ tasksOpen: false });
      }
      if (st.jobsOpen && !t.closest("#jobs-panel") && !t.closest("#btn-jobs")) {
        setState({ jobsOpen: false });
      }
      // 点聊天区用户头像(data-uav)→ 开「用户头像」弹窗(设置里也能改)。
      if (t.closest && t.closest("[data-uav]")) {
        setState({ avatarOpen: true });
      }

      // 点击聊天里的截图放大查看(简易全屏遮罩,再点关闭)
      const img = t.closest && t.closest(".msg-imgs img");
      if (img) {
        const ov = document.createElement("div");
        ov.style.cssText = "position:fixed;inset:0;background:rgba(15,23,42,.82);z-index:99;display:flex;align-items:center;justify-content:center;cursor:zoom-out;padding:32px;";
        ov.innerHTML = '<img style="max-width:100%;max-height:100%;border-radius:8px;" alt="截图">';
        ov.querySelector("img").src = img.src;
        ov.addEventListener("click", () => ov.remove());
        document.body.appendChild(ov);
      }
    };
    document.addEventListener("click", onDocClick);

    // 快捷键:Ctrl/⌘+K = 新建会话(样图上「新建会话」按钮旁的 K 角标就是它)。
    const onHotkey = (e) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && String(e.key).toLowerCase() === "k") {
        e.preventDefault();
        detachWorkerForNewSession();
        resetToNew();
      }
    };
    window.addEventListener("keydown", onHotkey);

    return () => {
      clearInterval(slow);
      document.removeEventListener("click", onDocClick);
      window.removeEventListener("keydown", onHotkey);
      disconnectEvents();
    };
  }, []);

  return html`<${Fragment}>
    <${Sidebar} />
    <${WorkerMenu} />
    <main>
      <${Topbar} />
      <section class="chat">
        <${ChatHead} />
        <${Chat} />
        <${Composer} />
        <${ContextPanel} />
        <${TrajPanel} />
        <${SummaryPanel} />
      </section>
    </main>
    <${UserAvatarModal} />
    <${Settings} />
    <${ApprovalModal} />
    <${WorkerNoteModal} />
    <${PluginLogsModal} />
    <${Toast} />
  </${Fragment}>`;
}

export default App;
