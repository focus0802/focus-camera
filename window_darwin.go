//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit -Wl,-sectcreate,__TEXT,__info_plist,${SRCDIR}/macos_info.plist

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>

static NSImage* createFocusCameraIcon(void) {
	NSSize size = NSMakeSize(256, 256);
	NSImage *image = [[NSImage alloc] initWithSize:size];
	[image lockFocus];

	NSGraphicsContext *ctx = [NSGraphicsContext currentContext];
	[ctx setShouldAntialias:YES];

	// Rounded squircle studio background
	NSRect bgRect = NSMakeRect(16, 16, 224, 224);
	NSBezierPath *bgPath = [NSBezierPath bezierPathWithRoundedRect:bgRect xRadius:52 yRadius:52];
	NSGradient *bgGrad = [[NSGradient alloc] initWithStartingColor:[NSColor colorWithSRGBRed:0.08 green:0.11 blue:0.17 alpha:1.0]
	                                                   endingColor:[NSColor colorWithSRGBRed:0.04 green:0.05 blue:0.08 alpha:1.0]];
	[bgGrad drawInBezierPath:bgPath angle:-90.0];

	// Subtle outer border
	[[NSColor colorWithSRGBRed:0.22 green:0.32 blue:0.48 alpha:0.55] setStroke];
	[bgPath setLineWidth:3.0];
	[bgPath stroke];

	// Outer camera lens ring
	NSRect outerRing = NSMakeRect(60, 60, 136, 136);
	NSBezierPath *outerPath = [NSBezierPath bezierPathWithOvalInRect:outerRing];
	[[NSColor colorWithSRGBRed:0.12 green:0.17 blue:0.26 alpha:1.0] setFill];
	[outerPath fill];
	[[NSColor colorWithSRGBRed:0.24 green:0.55 blue:0.96 alpha:0.9] setStroke];
	[outerPath setLineWidth:6.0];
	[outerPath stroke];

	// Inner glass iris
	NSRect innerRing = NSMakeRect(86, 86, 84, 84);
	NSBezierPath *innerPath = [NSBezierPath bezierPathWithOvalInRect:innerRing];
	NSGradient *lensGrad = [[NSGradient alloc] initWithStartingColor:[NSColor colorWithSRGBRed:0.18 green:0.46 blue:0.88 alpha:1.0]
	                                                     endingColor:[NSColor colorWithSRGBRed:0.06 green:0.14 blue:0.30 alpha:1.0]];
	[lensGrad drawInBezierPath:innerPath angle:-45.0];

	// Lens specular highlight
	NSRect glintRect = NSMakeRect(102, 134, 24, 24);
	NSBezierPath *glintPath = [NSBezierPath bezierPathWithOvalInRect:glintRect];
	[[NSColor colorWithSRGBRed:1.0 green:1.0 blue:1.0 alpha:0.38] setFill];
	[glintPath fill];

	// Live REC indicator dot in top-right corner
	NSRect recDot = NSMakeRect(190, 190, 22, 22);
	NSBezierPath *recPath = [NSBezierPath bezierPathWithOvalInRect:recDot];
	[[NSColor colorWithSRGBRed:0.95 green:0.24 blue:0.28 alpha:1.0] setFill];
	[recPath fill];

	[image unlockFocus];
	return image;
}

@interface FocusAppDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate, WKUIDelegate, WKNavigationDelegate>
@property (nonatomic, strong) NSWindow *window;
@property (nonatomic, strong) WKWebView *webView;
@property (nonatomic, copy) NSString *initialURL;
@property (nonatomic, assign) BOOL forceClose;
@end

@implementation FocusAppDelegate

- (void)setupMenuBar {
	NSMenu *menubar = [[NSMenu alloc] init];
	[NSApp setMainMenu:menubar];

	// 1. Application Menu
	NSMenuItem *appMenuItem = [[NSMenuItem alloc] init];
	[menubar addItem:appMenuItem];
	NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"Focus Camera"];
	[appMenu addItemWithTitle:@"关于 Focus Camera" action:@selector(orderFrontStandardAboutPanel:) keyEquivalent:@""];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItemWithTitle:@"隐藏 Focus Camera" action:@selector(hide:) keyEquivalent:@"h"];
	NSMenuItem *hideOthers = [appMenu addItemWithTitle:@"隐藏其他" action:@selector(hideOtherApplications:) keyEquivalent:@"h"];
	[hideOthers setKeyEquivalentModifierMask:(NSEventModifierFlagCommand | NSEventModifierFlagOption)];
	[appMenu addItemWithTitle:@"全部显示" action:@selector(unhideAllApplications:) keyEquivalent:@""];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItemWithTitle:@"退出 Focus Camera" action:@selector(terminate:) keyEquivalent:@"q"];
	[appMenuItem setSubmenu:appMenu];

	// 2. Edit Menu (Enables Cmd+C, Cmd+V, Cmd+X, Cmd+A, Cmd+Z inside WKWebView inputs)
	NSMenuItem *editMenuItem = [[NSMenuItem alloc] init];
	[menubar addItem:editMenuItem];
	NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"编辑"];
	[editMenu addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
	NSMenuItem *redoItem = [editMenu addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"z"];
	[redoItem setKeyEquivalentModifierMask:(NSEventModifierFlagCommand | NSEventModifierFlagShift)];
	[editMenu addItem:[NSMenuItem separatorItem]];
	[editMenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
	[editMenu addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
	[editMenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
	[editMenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
	[editMenuItem setSubmenu:editMenu];

	// 3. View Menu
	NSMenuItem *viewMenuItem = [[NSMenuItem alloc] init];
	[menubar addItem:viewMenuItem];
	NSMenu *viewMenu = [[NSMenu alloc] initWithTitle:@"视图"];
	NSMenuItem *reloadItem = [[NSMenuItem alloc] initWithTitle:@"重新加载界面" action:@selector(reloadWebView:) keyEquivalent:@"r"];
	[reloadItem setTarget:self];
	[viewMenu addItem:reloadItem];
	[viewMenu addItem:[NSMenuItem separatorItem]];
	NSMenuItem *fullScreenItem = [viewMenu addItemWithTitle:@"进入全屏" action:@selector(toggleFullScreen:) keyEquivalent:@"f"];
	[fullScreenItem setKeyEquivalentModifierMask:(NSEventModifierFlagCommand | NSEventModifierFlagControl)];
	[viewMenuItem setSubmenu:viewMenu];

	// 4. Window Menu
	NSMenuItem *windowMenuItem = [[NSMenuItem alloc] init];
	[menubar addItem:windowMenuItem];
	NSMenu *windowMenu = [[NSMenu alloc] initWithTitle:@"窗口"];
	[windowMenu addItemWithTitle:@"最小化" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
	[windowMenu addItemWithTitle:@"缩放" action:@selector(performZoom:) keyEquivalent:@""];
	[windowMenu addItem:[NSMenuItem separatorItem]];
	[windowMenu addItemWithTitle:@"关闭窗口" action:@selector(performClose:) keyEquivalent:@"w"];
	[windowMenuItem setSubmenu:windowMenu];
	[NSApp setWindowsMenu:windowMenu];
}

- (void)reloadWebView:(id)sender {
	if (self.webView) {
		[self.webView reload];
	}
}

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
	[self setupMenuBar];

	NSImage *icon = createFocusCameraIcon();
	if (icon) {
		[NSApp setApplicationIconImage:icon];
	}

	NSScreen *screen = [NSScreen mainScreen];
	NSRect visible = screen ? [screen visibleFrame] : NSMakeRect(0, 0, 1680, 980);
	CGFloat width = MIN(1680.0, visible.size.width * 0.95);
	CGFloat height = MIN(1000.0, visible.size.height * 0.93);
	CGFloat x = visible.origin.x + (visible.size.width - width) / 2.0;
	CGFloat y = visible.origin.y + (visible.size.height - height) / 2.0;

	NSWindowStyleMask style = NSWindowStyleMaskTitled |
	                          NSWindowStyleMaskClosable |
	                          NSWindowStyleMaskMiniaturizable |
	                          NSWindowStyleMaskResizable;

	self.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(x, y, width, height)
	                                          styleMask:style
	                                            backing:NSBackingStoreBuffered
	                                              defer:NO];
	[self.window setTitle:@"Focus Camera · 29路教室高清监控与双向对讲系统"];
	[self.window setMinSize:NSMakeSize(1100, 700)];
	[self.window setCollectionBehavior:NSWindowCollectionBehaviorFullScreenPrimary];
	[self.window setAppearance:[NSAppearance appearanceNamed:NSAppearanceNameDarkAqua]];
	[self.window setBackgroundColor:[NSColor colorWithSRGBRed:0.055 green:0.067 blue:0.090 alpha:1.0]];
	[self.window setDelegate:self];

	WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
	config.mediaTypesRequiringUserActionForPlayback = WKAudiovisualMediaTypeNone;
	@try {
		[config.preferences setValue:@YES forKey:@"developerExtrasEnabled"];
	} @catch (NSException *exception) {}

	self.webView = [[WKWebView alloc] initWithFrame:[[self.window contentView] bounds]
	                                  configuration:config];
	[self.webView setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];
	[self.webView setUIDelegate:self];
	[self.webView setNavigationDelegate:self];
	@try {
		[self.webView setValue:@NO forKey:@"drawsBackground"];
	} @catch (NSException *exception) {}

	[[self.window contentView] addSubview:self.webView];

	NSURL *url = [NSURL URLWithString:self.initialURL];
	if (url) {
		NSURLRequest *req = [NSURLRequest requestWithURL:url];
		[self.webView loadRequest:req];
	}

	[self.window makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
}

// Auto-grant Microphone permission for :8800 Two-Way Voice Intercom
- (void)webView:(WKWebView *)webView
requestMediaCapturePermissionForOrigin:(WKSecurityOrigin *)origin
initiatedByFrame:(WKFrameInfo *)frame
type:(WKMediaCaptureType)type
decisionHandler:(void (^)(WKPermissionDecision decision))decisionHandler API_AVAILABLE(macos(12.0)) {
	decisionHandler(WKPermissionDecisionGrant);
}

// Native macOS NSAlert sheet for window.confirm(...)
- (void)webView:(WKWebView *)webView
runJavaScriptConfirmPanelWithMessage:(NSString *)message
initiatedByFrame:(WKFrameInfo *)frame
completionHandler:(void (^)(BOOL result))completionHandler {
	NSAlert *alert = [[NSAlert alloc] init];
	[alert setMessageText:@"操作确认"];
	[alert setInformativeText:message ?: @""];
	[alert addButtonWithTitle:@"确定"];
	[alert addButtonWithTitle:@"取消"];
	[alert setAlertStyle:NSAlertStyleWarning];
	if (self.window) {
		[alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse returnCode) {
			completionHandler(returnCode == NSAlertFirstButtonReturn);
		}];
	} else {
		NSModalResponse resp = [alert runModal];
		completionHandler(resp == NSAlertFirstButtonReturn);
	}
}

// Native macOS NSAlert sheet for window.alert(...)
- (void)webView:(WKWebView *)webView
runJavaScriptAlertPanelWithMessage:(NSString *)message
initiatedByFrame:(WKFrameInfo *)frame
completionHandler:(void (^)(void))completionHandler {
	NSAlert *alert = [[NSAlert alloc] init];
	[alert setMessageText:@"系统提示"];
	[alert setInformativeText:message ?: @""];
	[alert addButtonWithTitle:@"好"];
	[alert setAlertStyle:NSAlertStyleInformational];
	if (self.window) {
		[alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse returnCode) {
			completionHandler();
		}];
	} else {
		[alert runModal];
		completionHandler();
	}
}

// Intercept MP4 download links (?download=1) and show native macOS NSSavePanel
- (void)webView:(WKWebView *)webView
decidePolicyForNavigationAction:(WKNavigationAction *)navigationAction
decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
	NSURL *url = navigationAction.request.URL;
	NSString *query = url.query ?: @"";
	if ([url.path isEqualToString:@"/api/recordings/file"] && [query containsString:@"download=1"]) {
		decisionHandler(WKNavigationActionPolicyCancel);
		[self saveRecordingFromURL:url];
		return;
	}
	decisionHandler(WKNavigationActionPolicyAllow);
}

- (void)saveRecordingFromURL:(NSURL *)url {
	NSString *suggestedName = @"recording.mp4";
	NSURLComponents *components = [NSURLComponents componentsWithURL:url resolvingAgainstBaseURL:NO];
	for (NSURLQueryItem *item in components.queryItems) {
		if ([item.name isEqualToString:@"path"] && item.value.length > 0) {
			suggestedName = [[item.value lastPathComponent] stringByRemovingPercentEncoding] ?: @"recording.mp4";
			break;
		}
	}

	NSSavePanel *panel = [NSSavePanel savePanel];
	[panel setTitle:@"导出 1080P 监控录像分片"];
	[panel setNameFieldStringValue:suggestedName];
	[panel setCanCreateDirectories:YES];

	void (^handleSave)(NSModalResponse) = ^(NSModalResponse result) {
		if (result != NSModalResponseOK || !panel.URL) {
			return;
		}
		NSURL *destURL = panel.URL;
		NSURLSessionDataTask *task = [[NSURLSession sharedSession] dataTaskWithURL:url
		                                                         completionHandler:^(NSData *data, NSURLResponse *response, NSError *error) {
			if (data && !error) {
				[data writeToURL:destURL atomically:YES];
			}
		}];
		[task resume];
	};

	if (self.window) {
		[panel beginSheetModalForWindow:self.window completionHandler:handleSave];
	} else {
		handleSave([panel runModal]);
	}
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
	if (self.forceClose) {
		return YES;
	}
	NSAlert *alert = [[NSAlert alloc] init];
	[alert setMessageText:@"需要保持后台继续录制监控吗？"];
	[alert setInformativeText:@"点击「最小化到程序坞」将在后台继续进行 29 路 1080P 自动录像；点击「彻底退出并停录」将保存当前录像分片并完全关闭软件。"];
	[alert addButtonWithTitle:@"最小化到程序坞（继续录像）"];
	[alert addButtonWithTitle:@"彻底退出并停录"];
	[alert addButtonWithTitle:@"取消"];
	[alert setAlertStyle:NSAlertStyleInformational];

	[alert beginSheetModalForWindow:sender completionHandler:^(NSModalResponse returnCode) {
		if (returnCode == NSAlertFirstButtonReturn) {
			[sender miniaturize:nil];
		} else if (returnCode == NSAlertSecondButtonReturn) {
			self.forceClose = YES;
			[NSApp terminate:nil];
		}
	}];
	return NO;
}

- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
	[NSApp stop:nil];
	NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
	                                    location:NSMakePoint(0, 0)
	                               modifierFlags:0
	                                   timestamp:0
	                                windowNumber:0
	                                     context:nil
	                                     subtype:0
	                                       data1:0
	                                       data2:0];
	[NSApp postEvent:event atStart:YES];
	return NSTerminateCancel;
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
	return YES;
}

- (void)windowWillClose:(NSNotification *)notification {
	[NSApp terminate:nil];
}

@end

static void runCocoaDesktopWindow(const char *cUrl) {
	@autoreleasepool {
		NSApplication *app = [NSApplication sharedApplication];
		[app setActivationPolicy:NSApplicationActivationPolicyRegular];

		FocusAppDelegate *delegate = [[FocusAppDelegate alloc] init];
		delegate.initialURL = [NSString stringWithUTF8String:cUrl];
		[app setDelegate:delegate];

		[app run];
	}
}
*/
import "C"
import "unsafe"

func runNativeDesktopWindow(appURL string) {
	cStr := C.CString(appURL)
	defer C.free(unsafe.Pointer(cStr))
	C.runCocoaDesktopWindow(cStr)
}
