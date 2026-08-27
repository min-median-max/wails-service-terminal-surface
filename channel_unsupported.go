//go:build !darwin

package terminalsurface

import "fmt"

// Channel exists on darwin; every other platform refuses by name (SPEC §2). A
// Windows or Linux transport arrives as its own change when a backend exists.
type Channel struct{}

func OpenChannel(identifier string) (*Channel, error) {
	return nil, fmt.Errorf("surface channel for %q: this platform has no mach bootstrap; darwin only", identifier)
}
