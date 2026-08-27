package terminalsurface

import compositor "github.com/min-median-max/wails-service-native-compositor"

// appKitRect is a frame in AppKit's bottom-left coordinates, in points.
type appKitRect struct{ X, Y, W, H float64 }

// placeRect converts one declared CSS top-left frame into the content view's bottom-left space.
// A negative span clamps to zero: AppKit treats a negative size as a huge unsigned one.
func placeRect(frame compositor.Frame, contentHeight float64) appKitRect {
	width, height := frame.Width, frame.Height
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	return appKitRect{X: frame.X, Y: contentHeight - frame.Y - height, W: width, H: height}
}
