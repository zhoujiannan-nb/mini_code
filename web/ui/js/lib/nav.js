// 极小的导航桥:让纯渲染模块(markdown)不必 import 整个 actions,
// 从而避免 actions ←→ markdown 的循环依赖。actions 在初始化时把实现填进来。
export const nav = {
  // (sessionId) => Promise<void> —— 由 actions.js 里的 selectSession 填充
  selectSession: null,
};
