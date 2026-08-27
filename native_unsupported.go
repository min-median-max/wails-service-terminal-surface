//go:build !darwin

package terminalsurface

import (
	"fmt"
	"unsafe"
)

// NewBackend wires the driver that fails by name: off darwin every operation refuses rather than
// reporting a pane as placed and leaving it blank, which would read as a broken plugin instead of
// a platform this build does not cover. The constructor itself cannot fail — the host builds its
// kind map unconditionally, the way the webview backend is built.
func NewBackend() *Backend { return newBackend(unsupportedDriver{}) }

type unsupportedDriver struct{}

func (unsupportedDriver) apply(unsafe.Pointer, []nativeOperation) ([]nativeResult, error) {
	return nil, fmt.Errorf("terminal surfaces have no driver on this platform")
}
func (unsupportedDriver) readPixels(unsafe.Pointer) ([]byte, error) {
	return nil, fmt.Errorf("terminal surfaces have no driver on this platform")
}
func (unsupportedDriver) focus(unsafe.Pointer) error {
	return fmt.Errorf("terminal surfaces have no driver on this platform")
}
