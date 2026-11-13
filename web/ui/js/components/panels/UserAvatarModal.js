// 用户头像弹窗:上传(jpg/png/webp,≤4MB)→ canvas 居中裁 256×256 JPEG → 存服务端配置。
import { h } from "preact";
import { useRef } from "preact/hooks";
import htm from "htm";

import { useStore, setState, toast } from "../../lib/store.js";
import { saveUserAvatar } from "../../lib/actions.js";
import { UserAvatar, fileToAvatarDataURL, validateAvatarFile } from "../../lib/avatars.js";

const html = htm.bind(h);

export function UserAvatarModal() {
  const open = useStore((s) => s.avatarOpen);
  const avatar = useStore((s) => s.userAvatar);
  const fileRef = useRef(null);

  const pick = async (e) => {
    const f = (e.target.files || [])[0];
    e.target.value = "";
    const bad = validateAvatarFile(f);
    if (bad) { if (f) toast(bad, "err"); return; }
    try {
      const dataURL = await fileToAvatarDataURL(f);
      await saveUserAvatar(dataURL);
      toast("头像已更新", "ok");
      setState({ avatarOpen: false });
    } catch (err) { toast("头像保存失败:" + err.message, "err"); }
  };

  const clear = async () => {
    if (!avatar) { toast("当前未设置头像", ""); return; }
    try {
      await saveUserAvatar("");
      toast("已移除头像(回退默认)", "ok");
      setState({ avatarOpen: false });
    } catch (e) { toast("移除失败:" + e.message, "err"); }
  };

  return html`<div class=${"modal-mask" + (open ? " open" : "")} id="user-avatar-mask"
    onClick=${(e) => { if (e.target.id === "user-avatar-mask") setState({ avatarOpen: false }); }}>
    <div class="modal modal-400">
      <h2>用户头像</h2>
      <div class="ua-body">
        <div id="ua-preview"><${UserAvatar} cls="ua-ava" /></div>
        <div class="ua-acts">
          <button class="send-btn" id="ua-upload" onClick=${() => fileRef.current && fileRef.current.click()}>上传头像</button>
          <button class="mode-btn" id="ua-clear" onClick=${clear}>移除头像</button>
        </div>
        <span class="hint u-center">支持 jpg / png / webp,≤4MB;自动居中裁为 256×256 方形<br />单聊、个人信息同步显示,重启不丢</span>
      </div>
      <div class="u-flex u-gap10 u-mt16">
        <button class="mode-btn" id="ua-close" onClick=${() => setState({ avatarOpen: false })}>关闭</button>
      </div>
    </div>
    <input type="file" id="ua-file" accept="image/jpeg,image/png,image/webp" hidden ref=${fileRef} onChange=${pick} />
  </div>`;
}
