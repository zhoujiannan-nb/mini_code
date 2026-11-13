// 入口:先应用主题与开机动画,再 mount(<App/>)。没有打包器 —— 浏览器靠
// index.html 的 import map 解析 "preact" / "preact/hooks" / "htm"。
//
// bootanim-player.js 必须是第一条 import:它在模块加载时读 #mc-boot-cfg 并立刻
// 铺黑/播放,晚一步就会先露出主界面。
import "./bootanim-player.js";

import { h, render } from "preact";
import { App } from "./components/App.js";
import { applyTheme, initThemeListener, readThemeMode } from "./lib/theme.js";
import { setState } from "./lib/store.js";

// 主题:index.html 不再有内联脚本,这里是"首屏前"最早能应用主题的位置。
applyTheme();
setState({ themeMode: readThemeMode() });
initThemeListener();

const root = document.getElementById("app");
render(h(App, {}), root);
