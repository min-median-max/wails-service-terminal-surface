package terminalsurface

import (
	"strings"
	"testing"
	"unsafe"

	compositor "github.com/min-median-max/wails-service-native-compositor"
)

// The driver is recorded, not run: these tests hold the inventory half of the backend, and the
// AppKit half is a darwin file behind the same interface.
type recordingDriver struct {
	batches [][]nativeOperation
	pixels  []byte
	focused int
}

func (driver *recordingDriver) apply(_ unsafe.Pointer, operations []nativeOperation) ([]nativeResult, error) {
	driver.batches = append(driver.batches, append([]nativeOperation(nil), operations...))
	results := make([]nativeResult, 0, len(operations))
	for _, operation := range operations {
		native := operation.native
		if operation.action == nativeCreate {
			native = unsafe.Pointer(new(byte))
		}
		if operation.action != nativeRemove {
			results = append(results, nativeResult{surface: operation.surface, native: native, window: operation.window})
		}
	}
	return results, nil
}

func (driver *recordingDriver) readPixels(unsafe.Pointer) ([]byte, error) {
	if driver.pixels == nil {
		driver.pixels = []byte("png")
	}
	return driver.pixels, nil
}

func (driver *recordingDriver) focus(unsafe.Pointer) error {
	driver.focused++
	return nil
}

func paneSurface(id string, generation uint64) compositor.Surface {
	return compositor.Surface{
		ID: id, Generation: generation, Kind: SurfaceKind,
		Frame:   compositor.Frame{X: 10, Y: 20, Width: 800, Height: 600},
		Visible: true, Alpha: 1,
		Source: compositor.SurfaceSource{"window": "win-abc123", "pane": "tab-abc123.1"},
	}
}

func snapshotOf(sequence uint64, surfaces ...compositor.Surface) compositor.Snapshot {
	return compositor.Snapshot{Window: "win-abc123", Sequence: sequence, Surfaces: surfaces}
}

func TestSurfaceKindIsTerminal(t *testing.T) {
	if SurfaceKind != "terminal" {
		t.Fatalf("the kind the plugin declares is %q", SurfaceKind)
	}
}

func TestApplyFillsTheWindowItWasHanded(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	applied, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Window == nil {
		t.Fatalf("applied surface reports no window: %+v", applied)
	}
}

func TestEmptyInventoryRemovesEveryOwner(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	applied, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("empty inventory left %d applied surfaces", len(applied))
	}
	if remove := lastActions(driver); len(remove) != 1 || remove[0] != nativeRemove {
		t.Fatalf("the owner was not removed: %v", remove)
	}
}

func TestGenerationChangeRecreatesThePane(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(2, paneSurface("terminal-1", 2))); err != nil {
		t.Fatal(err)
	}
	actions := lastActions(driver)
	if len(actions) != 2 || actions[0] != nativeRemove || actions[1] != nativeCreate {
		t.Fatalf("a generation change is remove then create: %v", actions)
	}
}

func TestEqualInventorySendsNoOperation(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	before := len(driver.batches)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(2, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	if len(driver.batches) != before {
		t.Fatalf("an unchanged inventory reached the driver: %v", driver.batches[len(driver.batches)-1])
	}
}

func TestDeliverUnknownVerbIsRefusedByName(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Deliver("terminal-1", map[string]any{"verb": "levitate"}); err == nil ||
		!strings.Contains(err.Error(), "levitate") {
		t.Fatalf("unknown verb was not refused by name: %v", err)
	}
}

func TestDeliverToAnUnownedSurfaceIsRefusedByName(t *testing.T) {
	backend := newBackend(&recordingDriver{})
	if _, err := backend.Deliver("terminal-9", map[string]any{"verb": "snapshot"}); err == nil ||
		!strings.Contains(err.Error(), "terminal-9") {
		t.Fatalf("unowned surface was not refused by name: %v", err)
	}
}

func TestDeliverSnapshotAnswersThePixels(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	answer, err := backend.Deliver("terminal-1", map[string]any{"verb": "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if answer["png"] == "" || answer["bytes"] != 3 {
		t.Fatalf("snapshot answered %v", answer)
	}
}

func TestDeliverFocusReachesTheDriver(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := byte(1)
	if _, err := backend.Apply(unsafe.Pointer(&window), snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Deliver("terminal-1", map[string]any{"verb": "focus"}); err != nil {
		t.Fatal(err)
	}
	if driver.focused != 1 {
		t.Fatalf("focus reached the driver %d times", driver.focused)
	}
}

func lastActions(driver *recordingDriver) []nativeAction {
	if len(driver.batches) == 0 {
		return nil
	}
	batch := driver.batches[len(driver.batches)-1]
	actions := make([]nativeAction, 0, len(batch))
	for _, operation := range batch {
		actions = append(actions, operation.action)
	}
	return actions
}
