package terminalsurface

import (
	"testing"

	compositor "github.com/min-median-max/wails-service-native-compositor"
)

// The declaration speaks CSS top-left points; AppKit speaks bottom-left. The conversion lives in
// one pure function so the flip is tested without a window.
func TestPlaceConvertsCssTopLeftToAppKit(t *testing.T) {
	rect := placeRect(compositor.Frame{X: 10, Y: 20, Width: 300, Height: 200}, 600)
	if rect.X != 10 || rect.Y != 380 || rect.W != 300 || rect.H != 200 {
		t.Fatalf("css (10,20,300x200) in a 600pt content view landed at %+v", rect)
	}
}

func TestPlaceClampsNegativeSpans(t *testing.T) {
	rect := placeRect(compositor.Frame{X: 5, Y: 5, Width: -40, Height: -1}, 600)
	if rect.W != 0 || rect.H != 0 {
		t.Fatalf("negative spans were not clamped: %+v", rect)
	}
}
