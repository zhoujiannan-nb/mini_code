// NOTE: webview.h is locally patched for macOS — WKWebView's UIDelegate is a
// weak reference, so the WebviewWKUIDelegate instance is retained (instead of
// autoreleased) for the process lifetime. Without this, the delegate is
// released when the enclosing autorelease pool drains and WKWebView file
// pickers (<input type="file">) and JS dialogs stop working.
#include "webview.h"
