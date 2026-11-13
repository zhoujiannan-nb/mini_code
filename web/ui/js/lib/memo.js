// 极简 memo:Preact 11 的 core 没有导出 memo(它在 preact/compat 里),而这次
// 重构不允许引入 compat —— 这里用 class 的 shouldComponentUpdate 实现同一个语义。
//
// 这是"换渲染引擎"后拿到的主要性能收益所在:消息列表按 key 复用组件实例,
// 流式 delta 只让"正在生成的那一条"重渲,历史消息的 Markdown 不会再被整条重算。
import { Component } from "preact";

function shallowEqual(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) {
    if (!Object.is(a[k], b[k])) return false;
  }
  return true;
}

// memo(Render, compare?):Render 必须是一个无 hook 的纯渲染函数。
export function memo(Render, compare) {
  return class Memoized extends Component {
    shouldComponentUpdate(nextProps) {
      return !(compare ? compare(this.props, nextProps) : shallowEqual(this.props, nextProps));
    }
    render(props) {
      return Render(props);
    }
  };
}
