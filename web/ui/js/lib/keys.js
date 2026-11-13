// 组件 key / 消息 id 的唯一来源:递增整数,不依赖随机数或时间,快照可复现。
let seq = 0;

export function newKey(prefix) {
  seq += 1;
  return (prefix || "k") + "-" + seq;
}

// 测试里需要"从零开始"时调用(生产路径不需要)。
export function resetKeys() {
  seq = 0;
}
