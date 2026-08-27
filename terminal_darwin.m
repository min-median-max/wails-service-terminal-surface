//go:build darwin

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#include "terminal_darwin.h"

// One pane's host: a layer-backed, clipping NSView whose layer contents will be the sidecar's
// IOSurface. It draws nothing itself.
@interface SoksakTerminalHostView : NSView
@end

@implementation SoksakTerminalHostView
- (BOOL)isFlipped { return NO; }
@end

static void soksakTerminalOnMain(void (^block)(void)) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }
}

int soksakTerminalSurfaceCreate(void *nsWindow, double x, double y, double w, double h,
                                bool visible, double alpha, void **outView) {
    __block int status = soksakTerminalStatusDone;
    __block SoksakTerminalHostView *host = nil;
    soksakTerminalOnMain(^{
        NSWindow *window = (__bridge NSWindow *)nsWindow;
        if (window == nil) { status = soksakTerminalStatusNoWindow; return; }
        NSView *content = window.contentView;
        if (content == nil) { status = soksakTerminalStatusNoContentView; return; }
        [CATransaction begin];
        [CATransaction setDisableActions:YES];
        host = [[SoksakTerminalHostView alloc] initWithFrame:NSMakeRect(x, y, w, h)];
        host.wantsLayer = YES;
        host.layer.masksToBounds = YES;
        host.layerContentsRedrawPolicy = NSViewLayerContentsRedrawDuringViewResize;
        host.layerContentsPlacement = NSViewLayerContentsPlacementTopLeft;
        host.autoresizingMask = NSViewNotSizable;
        host.hidden = !visible;
        host.alphaValue = alpha;
        [content addSubview:host positioned:NSWindowAbove relativeTo:nil];
        [CATransaction commit];
    });
    if (status == soksakTerminalStatusDone && host != nil) {
        *outView = (__bridge_retained void *)host;
    } else if (status == soksakTerminalStatusDone) {
        status = soksakTerminalStatusNoView;
    }
    return status;
}

int soksakTerminalSurfacePlace(void *view, double x, double y, double w, double h,
                               bool visible, double alpha) {
    __block int status = soksakTerminalStatusDone;
    soksakTerminalOnMain(^{
        SoksakTerminalHostView *host = (__bridge SoksakTerminalHostView *)view;
        if (host == nil) { status = soksakTerminalStatusNoView; return; }
        [CATransaction begin];
        [CATransaction setDisableActions:YES];
        NSRect wanted = NSMakeRect(x, y, w, h);
        if (!NSEqualRects(host.frame, wanted)) host.frame = wanted;
        if (host.hidden != !visible) host.hidden = !visible;
        if (host.alphaValue != alpha) host.alphaValue = alpha;
        [CATransaction commit];
    });
    return status;
}

int soksakTerminalSurfaceRemove(void *view) {
    __block int status = soksakTerminalStatusDone;
    soksakTerminalOnMain(^{
        SoksakTerminalHostView *host = (__bridge_transfer SoksakTerminalHostView *)view;
        if (host == nil) { status = soksakTerminalStatusNoView; return; }
        [CATransaction begin];
        [CATransaction setDisableActions:YES];
        [host removeFromSuperview];
        [CATransaction commit];
    });
    return status;
}

void *soksakTerminalSurfaceWindowOf(void *view) {
    __block void *window = NULL;
    soksakTerminalOnMain(^{
        SoksakTerminalHostView *host = (__bridge SoksakTerminalHostView *)view;
        window = (__bridge void *)host.window;
    });
    return window;
}

double soksakTerminalContentHeight(void *nsWindow) {
    __block double height = 0;
    soksakTerminalOnMain(^{
        NSWindow *window = (__bridge NSWindow *)nsWindow;
        height = NSHeight(window.contentView.bounds);
    });
    return height;
}
