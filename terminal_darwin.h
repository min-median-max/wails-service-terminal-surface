// The AppKit half of the terminal surface backend: one layer-backed host view per pane inside the
// window's content view, above the web view. The Objective-C lives in terminal_darwin.m as its own
// compilation unit; cgo keeps only the directives and this include.
#ifndef SOKSAK_TERMINAL_SURFACE_DARWIN_H
#define SOKSAK_TERMINAL_SURFACE_DARWIN_H

#include <stdbool.h>
#include <stddef.h>

// Status codes mirror the webview driver's convention: 0 is done, everything else names a refusal
// the Go side wraps with the pane id.
enum {
    soksakTerminalStatusDone = 0,
    soksakTerminalStatusNoWindow = 1,
    soksakTerminalStatusNoContentView = 2,
    soksakTerminalStatusNoView = 3,
};

// Creates one host view inside the window's content view, above every sibling, and answers the
// retained view pointer. Runs its AppKit work on the main thread whichever thread calls.
int soksakTerminalSurfaceCreate(void *nsWindow, double x, double y, double w, double h,
                                bool visible, double alpha, void **outView);

// Applies a frame, visibility and alpha to one host view. Writes the frame only when it differs:
// an unconditional write marks the layer for commit and stalls the window.
int soksakTerminalSurfacePlace(void *view, double x, double y, double w, double h,
                               bool visible, double alpha);

// Removes one host view from its window and releases it.
int soksakTerminalSurfaceRemove(void *view);

// Answers the NSWindow the view is actually inside — the fact misparented is judged by.
void *soksakTerminalSurfaceWindowOf(void *view);

#endif

// Answers the window's content view height in points, for the top-left conversion.
double soksakTerminalContentHeight(void *nsWindow);
