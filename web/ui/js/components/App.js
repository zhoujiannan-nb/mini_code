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
import { SuperFrame } from "./SuperPanel.js";
import { Settings } from "./Settings.js";
import { ApprovalModal } from "./panels/ApprovalModal.js";
import { UserAvatarModal } from "./panels/UserAvatarModal.js";
import { WorkerNoteModal, PluginLogsModal } from "./panels/NoteModal.js";
import { Toast } from "./Toast.js";

const html = htm.bind(h);

async function bootstrap() {
  initWorkerBar();
  await loadWorkers();
  await loadSessions();
  loadStatus();
  connectEvents();
  syncApprovals(); 
  await loadUserAvatar();  
  await loadAgentAvatar(); 
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

    const slow = setInterval(() => {
      loadStatus();
      loadSessions();
      loadWorkers();
      updateTokenUsage();
      syncApprovals();
    }, 30000);

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
      if (t.closest && t.closest("[data-uav]")) {
        setState({ avatarOpen: true });
      }

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
        <${SuperFrame}>
          <${Chat} />
          <${Composer} />
        <//>
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
