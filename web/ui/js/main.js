import "./bootanim-player.js";

import { h, render } from "preact";
import { App } from "./components/App.js";
import { applyTheme, initThemeListener, readThemeMode } from "./lib/theme.js";
import { initTitlebar } from "./lib/titlebar.js";
import { setState } from "./lib/store.js";

applyTheme();
setState({ themeMode: readThemeMode() });
initThemeListener();

initTitlebar();

const root = document.getElementById("app");
render(h(App, {}), root);
