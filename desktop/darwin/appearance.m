#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

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

// Install a standard main menu (app menu + edit menu).
//
// webview_go creates a bare Cocoa app with NO main menu, so the standard
// editing shortcuts (Cmd+C/V/X/A) have no menu item to trigger:
// AppKit resolves them via [NSApp mainMenu performKeyEquivalent:], and
// without a menu the key events never reach the WKWebView's paste:/copy:/
// cut:/selectAll: responder actions. This is what makes Cmd+V paste work
// in the web content. Idempotent; call before the first [NSApp run].
void mc_setup_main_menu(void) {
  if ([NSApp mainMenu] != nil) return;

  NSMenu *mainMenu = [[NSMenu alloc] init];

  // --- application menu (bold app-name menu; Cmd+Q quit) ---
  NSMenuItem *appItem = [mainMenu addItemWithTitle:@"" action:NULL keyEquivalent:@""];
  NSMenu *appMenu = [[NSMenu alloc] init];
  [appItem setSubmenu:appMenu];
  NSString *appName = @"Mini Code";
  [appMenu addItemWithTitle:[@"关于 " stringByAppendingString:appName]
                     action:@selector(orderFrontStandardAboutPanel:)
              keyEquivalent:@""];
  [appMenu addItem:[NSMenuItem separatorItem]];
  [appMenu addItemWithTitle:[@"退出 " stringByAppendingString:appName]
                     action:@selector(terminate:)
              keyEquivalent:@"q"];

  // --- edit menu (cut / copy / paste / select all) ---
  NSMenuItem *editItem = [mainMenu addItemWithTitle:@"编辑" action:NULL keyEquivalent:@""];
  NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"编辑"];
  [editItem setSubmenu:editMenu];
  [editMenu addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
  NSMenuItem *redo = [editMenu addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"Z"];
  [editMenu addItem:[NSMenuItem separatorItem]];
  [editMenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
  [editMenu addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
  [editMenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
  [editMenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];

  [NSApp setMainMenu:mainMenu];
}
