//go:build !darwin

package terminalsurface

import (
	"fmt"
	"unsafe"
)

// NewBackend fails by name off darwin: a nil driver would report a pane as placed and leave it
// blank, which reads as a broken plugin rather than a platform this build does not cover.
func NewBackend() (*Backend, error) {
	return nil, fmt.Errorf("terminal surfaces have no driver on this platform")
}

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
