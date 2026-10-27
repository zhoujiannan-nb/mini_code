#!/usr/bin/env python3
"""Tiny companion service for the `process-demo` plugin.

It is deliberately boring: an HTTP server that answers /healthz and /info. The
point is to exercise the *host* side of a kind-1 plugin — start, health check,
expose to the model, stop, restart — not to be useful by itself.

Writes one line to stderr on every request so the plugin centre's 日志 button has
something to show.
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ.get("PORT", "8731"))
PID = os.getpid()
PLUGIN_ID = os.environ.get("MINICODE_PLUGIN_ID", "?")


class Handler(BaseHTTPRequestHandler):
    def _send(self, payload, code=200):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        # 打一行日志:插件中心的"日志"按钮读的就是 stdout/stderr
        print("GET %s" % self.path, flush=True)
        if self.path == "/healthz":
            self._send({"ok": True, "pid": PID})
        elif self.path == "/info":
            self._send({
                "plugin": PLUGIN_ID,
                "pid": PID,
                "python": sys.version.split()[0],
                "message": "process-demo 在跑;模型可以通过 plugin_proc__process_demo 查到它。",
            })
        else:
            self._send({"ok": False, "error": "not found"}, 404)

    def log_message(self, fmt, *args):
        # 默认实现往 stderr 写一行带时间戳的日志,正合我们的需要
        sys.stderr.write("%s - %s\n" % (PLUGIN_ID, fmt % args))
        sys.stderr.flush()


if __name__ == "__main__":
    print("process-demo listening on 127.0.0.1:%d" % PORT, flush=True)
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
