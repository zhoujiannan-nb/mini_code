"use strict";
const $ = (s) => document.querySelector(s);

// 浏览器里记住当前会话,刷新页面后自动恢复到刷新前打开的会话
const LS_SESSION_KEY = "mini_code.current_session";

