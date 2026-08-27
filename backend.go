// Package terminalsurface places sidecar-rendered terminal surfaces in the windows that declared
// them. It implements Backend for wails-service-native-compositor: the inventory half diffs
// declarations against owners here, and the AppKit half lives behind nativeDriver in a darwin
// compilation unit. This service holds no cell, no glyph and no atlas — the render sidecar that
// owns the grid owns the pixels.
package terminalsurface

import (
	"encoding/base64"
	"fmt"
	"sort"
	"unsafe"

	compositor "github.com/min-median-max/wails-service-native-compositor"
)

// SurfaceKind is the word the plugin's declaration carries in data-native-surface.
const SurfaceKind compositor.SurfaceKind = "terminal"

type nativeAction byte

const (
	nativeCreate nativeAction = iota
	nativeUpdate
	nativeRemove
)

type nativeOperation struct {
	action  nativeAction
	surface compositor.Surface
	native  unsafe.Pointer
	window  unsafe.Pointer
}

type nativeResult struct {
	surface compositor.Surface
	native  unsafe.Pointer
	window  unsafe.Pointer
}

// nativeDriver is the AppKit half: a host view per pane, its layer contents, and the pixels the
// parking picture reads. Everything else the compositor asks for is answered from the inventory.
type nativeDriver interface {
	apply(window unsafe.Pointer, operations []nativeOperation) ([]nativeResult, error)
	readPixels(native unsafe.Pointer) ([]byte, error)
	focus(native unsafe.Pointer) error
}

type nativeOwner struct {
	generation uint64
	native     unsafe.Pointer
	window     unsafe.Pointer
	surface    compositor.Surface
}

// Backend is the terminal surface kind.
type Backend struct {
	driver nativeDriver
	owners map[string]nativeOwner
}

func newBackend(driver nativeDriver) *Backend {
	return &Backend{driver: driver, owners: make(map[string]nativeOwner)}
}

// Apply reconciles one window's declared terminal surfaces against the owners this backend holds.
// The whole inventory arrives every commit, so what is absent is removed, what is new is created,
// and what changed is updated — an unchanged inventory never reaches the driver.
func (backend *Backend) Apply(window unsafe.Pointer, snapshot compositor.Snapshot) ([]compositor.AppliedSurface, error) {
	operations := planBatch(backend.owners, window, snapshot)
	if len(operations) > 0 {
		results, err := backend.driver.apply(window, operations)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			if result.native == nil {
				return nil, fmt.Errorf("terminal surface owner is empty: %s", result.surface.ID)
			}
			backend.owners[result.surface.ID] = nativeOwner{
				generation: result.surface.Generation,
				native:     result.native,
				window:     result.window,
				surface:    result.surface,
			}
		}
		for _, operation := range operations {
			if operation.action == nativeRemove {
				delete(backend.owners, operation.surface.ID)
			}
		}
	} else {
		// Nothing moved; refresh the remembered declarations so the receipt echoes this commit.
		for _, surface := range snapshot.Surfaces {
			owner := backend.owners[surface.ID]
			owner.surface = surface
			backend.owners[surface.ID] = owner
		}
	}
	applied := make([]compositor.AppliedSurface, 0, len(snapshot.Surfaces))
	for _, surface := range snapshot.Surfaces {
		owner, held := backend.owners[surface.ID]
		if !held {
			continue
		}
		applied = append(applied, compositor.AppliedSurface{
			ID: surface.ID, Generation: owner.generation, Frame: surface.Frame,
			Visible: surface.Visible, Alpha: surface.Alpha, Layer: surface.Layer,
			Window: owner.window,
		})
	}
	return applied, nil
}

// Deliver hands one verb to one owned pane. An unknown verb and an unowned id are refused with
// their names; a verb the surface channel will serve arrives in the channel change.
func (backend *Backend) Deliver(id string, message map[string]any) (map[string]any, error) {
	owner, held := backend.owners[id]
	if !held {
		return nil, fmt.Errorf("terminal surface %s is not applied", id)
	}
	verb, _ := message["verb"].(string)
	switch verb {
	case "snapshot":
		pixels, err := backend.driver.readPixels(owner.native)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"png":   base64.StdEncoding.EncodeToString(pixels),
			"bytes": len(pixels),
		}, nil
	case "focus":
		if err := backend.driver.focus(owner.native); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("terminal surface verb %q is not served", verb)
}

// planBatch orders create, update and remove for one commit. Layer then id, so two runs of the
// same inventory hand the driver the same order.
func planBatch(current map[string]nativeOwner, window unsafe.Pointer, snapshot compositor.Snapshot) []nativeOperation {
	desired := append([]compositor.Surface(nil), snapshot.Surfaces...)
	sort.Slice(desired, func(left, right int) bool {
		if desired[left].Layer != desired[right].Layer {
			return desired[left].Layer < desired[right].Layer
		}
		return desired[left].ID < desired[right].ID
	})
	var operations []nativeOperation
	seen := make(map[string]bool, len(desired))
	for _, surface := range desired {
		seen[surface.ID] = true
		owner, held := current[surface.ID]
		switch {
		case held && owner.generation != surface.Generation:
			operations = append(operations,
				nativeOperation{action: nativeRemove, surface: owner.surface, native: owner.native, window: window},
				nativeOperation{action: nativeCreate, surface: surface, window: window})
		case held:
			if !placementEqual(owner.surface, surface) {
				operations = append(operations, nativeOperation{action: nativeUpdate, surface: surface, native: owner.native, window: window})
			}
		default:
			operations = append(operations, nativeOperation{action: nativeCreate, surface: surface, window: window})
		}
	}
	removed := make([]string, 0)
	for id := range current {
		if !seen[id] {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	for _, id := range removed {
		owner := current[id]
		operations = append(operations, nativeOperation{action: nativeRemove, surface: owner.surface, native: owner.native, window: window})
	}
	return operations
}

// placementEqual reports whether a declaration changed anything the driver applies. Writing an
// unchanged frame marks the layer for commit and stalls the window, so equality is checked here
// rather than trusted to the driver.
func placementEqual(before, after compositor.Surface) bool {
	return before.Frame == after.Frame &&
		before.Visible == after.Visible &&
		before.Alpha == after.Alpha &&
		before.Layer == after.Layer &&
		sourceEqual(before.Source, after.Source)
}

func sourceEqual(before, after compositor.SurfaceSource) bool {
	if len(before) != len(after) {
		return false
	}
	for key, value := range before {
		if after[key] != value {
			return false
		}
	}
	return true
}
