// 截图(粘贴/选择 → 缩放 → data URL)的纯前端部分。
// 旧 composer-image.js 的实现原样保留(canvas 缩放、PNG 透明通道、JPEG 0.85),
// 只是不再直接改 state 和 DOM —— 待发图片由 Composer 组件的 state 持有。

export const MAX_IMAGES = 4;
export const MAX_IMAGE_DIM = 1280;            // 前端缩放到这个尺寸,控制 base64 体积和 token
export const MAX_IMAGE_BYTES = 4 * 1024 * 1024; // 单张 data URL 上限(前端先拦一道)

// 把图片文件缩放后转成 data URL。PNG 且不大时保留原格式(截图常带透明通道),
// 否则统一压成 JPEG 0.85(体积最小)。
export function fileToDataURL(file) {
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
