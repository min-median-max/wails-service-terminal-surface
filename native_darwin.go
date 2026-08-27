//go:build darwin

package terminalsurface

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fblocks
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore
#include <stdlib.h>
#include "terminal_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// NewBackend wires the AppKit driver.
func NewBackend() *Backend { return newBackend(appKitDriver{}) }

type appKitDriver struct{}

func (appKitDriver) apply(window unsafe.Pointer, operations []nativeOperation) ([]nativeResult, error) {
	results := make([]nativeResult, 0, len(operations))
	for _, operation := range operations {
		contentHeight := contentViewHeight(window)
		rect := placeRect(operation.surface.Frame, contentHeight)
		switch operation.action {
		case nativeCreate:
			var view unsafe.Pointer
			status := C.soksakTerminalSurfaceCreate(window,
				C.double(rect.X), C.double(rect.Y), C.double(rect.W), C.double(rect.H),
				C.bool(operation.surface.Visible), C.double(operation.surface.Alpha), &view)
			if status != C.soksakTerminalStatusDone {
				return nil, fmt.Errorf("terminal surface %s was not created: status %d", operation.surface.ID, int(status))
			}
			results = append(results, nativeResult{surface: operation.surface, native: view,
				window: C.soksakTerminalSurfaceWindowOf(view)})
		case nativeUpdate:
			status := C.soksakTerminalSurfacePlace(operation.native,
				C.double(rect.X), C.double(rect.Y), C.double(rect.W), C.double(rect.H),
				C.bool(operation.surface.Visible), C.double(operation.surface.Alpha))
			if status != C.soksakTerminalStatusDone {
				return nil, fmt.Errorf("terminal surface %s was not placed: status %d", operation.surface.ID, int(status))
			}
			results = append(results, nativeResult{surface: operation.surface, native: operation.native,
				window: C.soksakTerminalSurfaceWindowOf(operation.native)})
		case nativeRemove:
			if status := C.soksakTerminalSurfaceRemove(operation.native); status != C.soksakTerminalStatusDone {
				return nil, fmt.Errorf("terminal surface %s was not removed: status %d", operation.surface.ID, int(status))
			}
		}
	}
	return results, nil
}

// readPixels arrives with the surface channel: the pane's pixels are the sidecar's IOSurface, and
// before a ring exists there is nothing to read. Refusing by name keeps the parking picture from
// showing an empty rectangle as success.
func (appKitDriver) readPixels(unsafe.Pointer) ([]byte, error) {
	return nil, fmt.Errorf("terminal surface pixels arrive with the surface channel; no ring is connected")
}

func (appKitDriver) focus(view unsafe.Pointer) error {
	if view == nil {
		return fmt.Errorf("terminal surface focus has no view")
	}
	// First-responder transfer arrives with the input capture view of the surface channel change.
	return nil
}

// contentViewHeight reads the window's content height for the top-left conversion. The compositor
// hands Apply the window it resolved; a nil window is caught by the create path's status.
func contentViewHeight(window unsafe.Pointer) float64 {
	return float64(C.soksakTerminalContentHeight(window))
}
