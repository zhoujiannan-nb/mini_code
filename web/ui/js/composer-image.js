/* ---------------- 截图(粘贴/选择 → 缩放 → 预览) ---------------- */
const MAX_IMAGES = 4;
const MAX_IMAGE_DIM = 1280;      // 前端缩放到这个尺寸,控制 base64 体积和 token
const MAX_IMAGE_BYTES = 4 * 1024 * 1024; // 单张 data URL 上限(前端先拦一道)

// 把图片文件缩放后转成 data URL。PNG 且不大时保留原格式(截图常带透明通道),
// 否则统一压成 JPEG 0.85(体积最小)。
function fileToDataURL(file) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      let w = img.naturalWidth, h = img.naturalHeight;
      if (w > MAX_IMAGE_DIM || h > MAX_IMAGE_DIM) {
        const k = Math.min(MAX_IMAGE_DIM / w, MAX_IMAGE_DIM / h);
        w = Math.round(w * k); h = Math.round(h * k);
      }
      const canvas = document.createElement("canvas");
      canvas.width = w; canvas.height = h;
      const ctx = canvas.getContext("2d");
      ctx.fillStyle = "#fff";
      ctx.fillRect(0, 0, w, h); // JPEG 无透明通道,先铺白底
      ctx.drawImage(img, 0, 0, w, h);
      const keepPng = file.type === "image/png" && file.size < 512 * 1024;
      const dataUrl = keepPng ? canvas.toDataURL("image/png")
                              : canvas.toDataURL("image/jpeg", 0.85);
      resolve(dataUrl);
    };
    img.onerror = () => { URL.revokeObjectURL(url); reject(new Error("图片读取失败")); };
    img.src = url;
  });
}

async function addImageFiles(files) {
  for (const f of files) {
    if (!f || !f.type.startsWith("image/")) continue;
    if (state.pendingImages.length >= MAX_IMAGES) {
      toast("最多 " + MAX_IMAGES + " 张截图", "err");
      break;
    }
    try {
      const dataUrl = await fileToDataURL(f);
      if (dataUrl.length > MAX_IMAGE_BYTES) { toast("图片过大,已跳过", "err"); continue; }
      state.pendingImages.push(dataUrl);
    } catch (e) { toast(e.message, "err"); }
  }
  renderImgPreviews();
}

function renderImgPreviews() {
  const box = $("#img-previews");
  box.innerHTML = "";
  state.pendingImages.forEach((u, i) => {
    const d = document.createElement("div");
    d.className = "img-prev";
    d.innerHTML = '<img src="' + u + '" alt="截图">' +
      '<button class="rm" title="移除">×</button>';
    d.querySelector(".rm").addEventListener("click", () => {
      state.pendingImages.splice(i, 1);
      renderImgPreviews();
    });
    box.appendChild(d);
  });
  box.hidden = state.pendingImages.length === 0;
}

function readSavedSession() {
  try { return localStorage.getItem(LS_SESSION_KEY); } catch (e) { return null; }
}
function saveSession(id) {
  try { id ? localStorage.setItem(LS_SESSION_KEY, id) : localStorage.removeItem(LS_SESSION_KEY); } catch (e) {}
}

