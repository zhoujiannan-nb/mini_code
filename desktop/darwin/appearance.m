#import <Cocoa/Cocoa.h>

// Switch the whole app (and therefore the window title bar) between dark
// and light appearance. Called from the UI thread via webview Dispatch.
void mc_set_app_dark(int dark) {
  [NSApp setAppearance:dark
                   ? [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua]
                   : [NSAppearance appearanceNamed:NSAppearanceNameAqua]];
}

// Center the window on its screen after creation (the webview library
// creates it at the default cascading position).
void mc_center_window(void *w) {
  [(__bridge NSWindow *)w center];
}

// Make a hidden window key and frontmost and activate the app.
void mc_show_window(void *w) {
  NSWindow *win = (__bridge NSWindow *)w;
  [win makeKeyAndOrderFront:nil];
  [NSApp activateIgnoringOtherApps:YES];
}
