
export const MAX_IMAGES = 4;
export const MAX_IMAGE_DIM = 1280;            
export const MAX_IMAGE_BYTES = 4 * 1024 * 1024; 

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
      ctx.fillRect(0, 0, w, h); 
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
