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
	action              nativeAction
	surface             compositor.Surface
	native              unsafe.Pointer
	window              unsafe.Pointer
	lifecycleGeneration uint64
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
	generation          uint64
	lifecycleGeneration uint64
	native              unsafe.Pointer
	window              unsafe.Pointer
	surface             compositor.Surface
}

// paneChannel is what the surface channel offers the inventory: a pane's host
// view binds when its owner appears and unbinds when the owner goes. The ring
// and its surfaces stay the sidecar's either way. The parking picture reads
// the displayed surface's own pixels from here.
type paneChannel interface {
	BindView(pane string, view unsafe.Pointer)
	UnbindView(pane string)
	SnapshotPNG(pane string) ([]byte, error)
}

// paneVerbs is the session layer behind the delivered verbs: input, reading,
// forwarded surface commands, held numbers and the stop path.
type paneVerbs interface {
	Input(pane, data string) error
	Read(pane string, lines int) (string, error)
	Forward(pane, command string, request map[string]any) (map[string]any, error)
	State(pane string) (map[string]any, error)
	Stop(pane, intent string) error
	Resize(pane string, pixelW, pixelH, scale float64) error
}

// Backend is the terminal surface kind.
type Backend struct {
	driver                  nativeDriver
	owners                  map[string]nativeOwner
	channel                 paneChannel
	verbs                   paneVerbs
	onPane                  func(created bool, lifecycleGeneration, declarationGeneration uint64, source compositor.SurfaceSource)
	nextLifecycleGeneration uint64
}

func newBackend(driver nativeDriver) *Backend {
	return &Backend{driver: driver, owners: make(map[string]nativeOwner)}
}

// UseChannel connects the surface channel. Without one the inventory still
// reconciles — the pane just has no pixels yet.
func (backend *Backend) UseChannel(channel paneChannel) { backend.channel = channel }

// UseSessions connects the pane session layer behind the delivered verbs.
func (backend *Backend) UseSessions(verbs paneVerbs) { backend.verbs = verbs }

// ObservePanes reports a pane's declaration appearing or going, with its
// source. The host runs the session opening off this — never on this path,
// which is the commit path.
func (backend *Backend) ObservePanes(observe func(
	created bool, lifecycleGeneration, declarationGeneration uint64, source compositor.SurfaceSource,
)) {
	backend.onPane = observe
}

// Apply reconciles one window's declared terminal surfaces against the owners this backend holds.
// The whole inventory arrives every commit, so what is absent is removed, what is new is created,
// and what changed is updated — an unchanged inventory never reaches the driver.
func (backend *Backend) Apply(window unsafe.Pointer, snapshot compositor.Snapshot) ([]compositor.AppliedSurface, error) {
	operations := planBatch(backend.owners, window, snapshot)
	if len(operations) > 0 {
		// The channel borrows each native view pointer. Detach every pointer while it is still
		// retained by the backend, before the driver transfers and releases native ownership.
		// An in-flight frame takes its own short lease; a later frame observes no bound view.
		if backend.channel != nil {
			for _, operation := range operations {
				if operation.action == nativeRemove {
					if pane := operation.surface.Source["pane"]; pane != "" {
						backend.channel.UnbindView(pane)
					}
				}
			}
		}
		results, err := backend.driver.apply(window, operations)
		if err != nil {
			return nil, err
		}
		creating := make(map[string]bool)
		for _, operation := range operations {
			if operation.action == nativeRemove {
				delete(backend.owners, operation.surface.ID)
			}
			if operation.action == nativeCreate {
				creating[operation.surface.ID] = true
			}
		}
		for _, result := range results {
			if result.native == nil {
				return nil, fmt.Errorf("terminal surface owner is empty: %s", result.surface.ID)
			}
			lifecycleGeneration := uint64(0)
			if creating[result.surface.ID] {
				backend.nextLifecycleGeneration++
				lifecycleGeneration = backend.nextLifecycleGeneration
			} else if held := backend.owners[result.surface.ID]; held.native != nil {
				lifecycleGeneration = held.lifecycleGeneration
			}
			backend.owners[result.surface.ID] = nativeOwner{
				generation:          result.surface.Generation,
				lifecycleGeneration: lifecycleGeneration,
				native:              result.native,
				window:              result.window,
				surface:             result.surface,
			}
		}
		for _, operation := range operations {
			pane := operation.surface.Source["pane"]
			if pane == "" {
				continue
			}
			switch operation.action {
			case nativeCreate:
				if backend.channel != nil {
					if owner, held := backend.owners[operation.surface.ID]; held {
						backend.channel.BindView(pane, owner.native)
					}
				}
				if backend.onPane != nil {
					backend.onPane(
						true, backend.owners[operation.surface.ID].lifecycleGeneration,
						operation.surface.Generation, operation.surface.Source,
					)
				}
			case nativeRemove:
				if backend.onPane != nil {
					backend.onPane(
						false, operation.lifecycleGeneration, operation.surface.Generation, operation.surface.Source,
					)
				}
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
	pane := owner.surface.Source["pane"]
	switch verb {
	case "snapshot":
		// The displayed IOSurface is the truthful picture; without a ring the
		// driver answers, and off darwin that answer is a named refusal.
		if backend.channel != nil {
			if pixels, err := backend.channel.SnapshotPNG(pane); err == nil {
				return map[string]any{
					"png":   base64.StdEncoding.EncodeToString(pixels),
					"bytes": len(pixels),
				}, nil
			}
		}
		pixels, err := backend.driver.readPixels(owner.native)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"png":   base64.StdEncoding.EncodeToString(pixels),
			"bytes": len(pixels),
		}, nil
	case "focus":
		focused, ok := message["focused"].(bool)
		if !ok {
			return nil, fmt.Errorf("terminal surface focus requires focused boolean")
		}
		if backend.verbs == nil {
			return nil, fmt.Errorf("terminal surface verb %q has no session layer; the host wired none", verb)
		}
		if focused {
			if err := backend.driver.focus(owner.native); err != nil {
				return nil, err
			}
		}
		return backend.verbs.Forward(pane, "surface.focus", map[string]any{"focused": focused})
	}
	if backend.verbs == nil {
		return nil, fmt.Errorf("terminal surface verb %q has no session layer; the host wired none", verb)
	}
	switch verb {
	case "input":
		data, _ := message["data"].(string)
		if err := backend.verbs.Input(pane, data); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "read":
		lines := 0
		if number, isNumber := message["lines"].(float64); isNumber && number > 0 {
			lines = int(number)
		}
		text, err := backend.verbs.Read(pane, lines)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
	case "scroll", "pointer", "wheel", "theme", "selection":
		request := make(map[string]any, len(message))
		for key, value := range message {
			if key != "verb" {
				request[key] = value
			}
		}
		return backend.verbs.Forward(pane, "surface."+verb, request)
	case "state":
		return backend.verbs.State(pane)
	case "resize":
		pixelW, _ := message["pixelW"].(float64)
		pixelH, _ := message["pixelH"].(float64)
		scale, _ := message["scale"].(float64)
		if pixelW <= 0 || pixelH <= 0 || scale <= 0 {
			return nil, fmt.Errorf("resize needs positive pixelW, pixelH and scale")
		}
		if err := backend.verbs.Resize(pane, pixelW, pixelH, scale); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "stop":
		intent, _ := message["intent"].(string)
		if err := backend.verbs.Stop(pane, intent); err != nil {
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
				nativeOperation{action: nativeRemove, surface: owner.surface, native: owner.native, window: window, lifecycleGeneration: owner.lifecycleGeneration},
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
		operations = append(operations, nativeOperation{action: nativeRemove, surface: owner.surface, native: owner.native, window: window, lifecycleGeneration: owner.lifecycleGeneration})
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
