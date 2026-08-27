package terminalsurface

import (
	"fmt"
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

type recordingChannel struct {
	binds   []string
	unbinds []string
	png     []byte
}

func (channel *recordingChannel) BindView(pane string, _ unsafe.Pointer) {
	channel.binds = append(channel.binds, pane)
}

func (channel *recordingChannel) UnbindView(pane string) {
	channel.unbinds = append(channel.unbinds, pane)
}

func (channel *recordingChannel) SnapshotPNG(string) ([]byte, error) {
	if channel.png == nil {
		return nil, refusal("no ring is displayed")
	}
	return channel.png, nil
}

func TestApplyBindsThePaneViewToTheChannel(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	channel := &recordingChannel{}
	backend.UseChannel(channel)
	window := unsafe.Pointer(new(byte))
	if _, err := backend.Apply(window, snapshotOf(1, paneSurface("s1", 7))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(channel.binds) != 1 || channel.binds[0] != "tab-abc123.1" {
		t.Fatalf("the created pane did not bind its view: %v", channel.binds)
	}
	if _, err := backend.Apply(window, snapshotOf(2)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(channel.unbinds) != 1 || channel.unbinds[0] != "tab-abc123.1" {
		t.Fatalf("the removed pane did not unbind: %v", channel.unbinds)
	}
}


type recordingVerbs struct {
	resizes  []string
	inputs   []string
	stops    []string
	forwards []string
}

func (verbs *recordingVerbs) Input(pane, data string) error {
	verbs.inputs = append(verbs.inputs, pane+":"+data)
	return nil
}

func (verbs *recordingVerbs) Read(string, int) (string, error) { return "text\n", nil }

func (verbs *recordingVerbs) Forward(pane, command string, _ map[string]any) (map[string]any, error) {
	verbs.forwards = append(verbs.forwards, pane+":"+command)
	return map[string]any{"offset": 1}, nil
}

func (verbs *recordingVerbs) State(string) (map[string]any, error) {
	return map[string]any{"phase": "live"}, nil
}

func (verbs *recordingVerbs) Resize(pane string, pixelW, pixelH, scale float64) error {
	verbs.resizes = append(verbs.resizes, fmt.Sprintf("%s:%gx%g@%g", pane, pixelW, pixelH, scale))
	return nil
}

func (verbs *recordingVerbs) Stop(pane, intent string) error {
	verbs.stops = append(verbs.stops, pane+":"+intent)
	return nil
}

func appliedBackend(t *testing.T) (*Backend, *recordingDriver, *recordingVerbs) {
	t.Helper()
	driver := &recordingDriver{}
	backend := newBackend(driver)
	verbs := &recordingVerbs{}
	backend.UseSessions(verbs)
	window := unsafe.Pointer(new(byte))
	if _, err := backend.Apply(window, snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	return backend, driver, verbs
}

func TestDeliverInputReachesTheSessionLayer(t *testing.T) {
	backend, _, verbs := appliedBackend(t)
	if _, err := backend.Deliver("terminal-1", map[string]any{"verb": "input", "data": "ls\r"}); err != nil {
		t.Fatal(err)
	}
	if len(verbs.inputs) != 1 || verbs.inputs[0] != "tab-abc123.1:ls\r" {
		t.Fatalf("input did not reach the sessions: %v", verbs.inputs)
	}
}

func TestDeliverScrollForwardsTheSurfaceCommand(t *testing.T) {
	backend, _, verbs := appliedBackend(t)
	answer, err := backend.Deliver("terminal-1", map[string]any{"verb": "scroll", "lines": 3.0})
	if err != nil || answer["offset"] != 1 {
		t.Fatalf("scroll answered %v, %v", answer, err)
	}
	if len(verbs.forwards) != 1 || verbs.forwards[0] != "tab-abc123.1:surface.scroll" {
		t.Fatalf("scroll did not forward: %v", verbs.forwards)
	}
}

func TestDeliverStateAndReadAnswer(t *testing.T) {
	backend, _, _ := appliedBackend(t)
	state, err := backend.Deliver("terminal-1", map[string]any{"verb": "state"})
	if err != nil || state["phase"] != "live" {
		t.Fatalf("state answered %v, %v", state, err)
	}
	read, err := backend.Deliver("terminal-1", map[string]any{"verb": "read", "lines": 4.0})
	if err != nil || read["text"] != "text\n" {
		t.Fatalf("read answered %v, %v", read, err)
	}
}

func TestDeliverStopCarriesTheIntent(t *testing.T) {
	backend, _, verbs := appliedBackend(t)
	if _, err := backend.Deliver("terminal-1", map[string]any{"verb": "stop", "intent": "close"}); err != nil {
		t.Fatal(err)
	}
	if len(verbs.stops) != 1 || verbs.stops[0] != "tab-abc123.1:close" {
		t.Fatalf("stop did not carry the intent: %v", verbs.stops)
	}
}

func TestDeliverWithoutSessionsRefusesByName(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	window := unsafe.Pointer(new(byte))
	if _, err := backend.Apply(window, snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Deliver("terminal-1", map[string]any{"verb": "input", "data": "x"}); err == nil ||
		!strings.Contains(err.Error(), "input") {
		t.Fatalf("a missing session layer was not refused by name: %v", err)
	}
}

func TestSnapshotPrefersTheDisplayedSurface(t *testing.T) {
	backend, _, _ := appliedBackend(t)
	channel := &recordingChannel{png: []byte("PNGBYTES")}
	backend.UseChannel(channel)
	answer, err := backend.Deliver("terminal-1", map[string]any{"verb": "snapshot"})
	if err != nil || answer["bytes"] != 8 {
		t.Fatalf("snapshot answered %v, %v", answer, err)
	}
}

func TestSnapshotFallsBackWhenNoRingIsDisplayed(t *testing.T) {
	backend, driver, _ := appliedBackend(t)
	backend.UseChannel(&recordingChannel{})
	driver.pixels = []byte("fallback")
	answer, err := backend.Deliver("terminal-1", map[string]any{"verb": "snapshot"})
	if err != nil || answer["bytes"] != 8 {
		t.Fatalf("snapshot did not fall back to the driver: %v, %v", answer, err)
	}
}

func TestObservePanesSeesCreateAndRemove(t *testing.T) {
	driver := &recordingDriver{}
	backend := newBackend(driver)
	var events []string
	backend.ObservePanes(func(created bool, source compositor.SurfaceSource) {
		events = append(events, source["pane"]+":"+map[bool]string{true: "created", false: "removed"}[created])
	})
	window := unsafe.Pointer(new(byte))
	if _, err := backend.Apply(window, snapshotOf(1, paneSurface("terminal-1", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Apply(window, snapshotOf(2)); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != "tab-abc123.1:created" || events[1] != "tab-abc123.1:removed" {
		t.Fatalf("pane observation saw %v", events)
	}
}

// A resize verb with the pane's new pixel box reaches the session layer.
func TestDeliverResizeReachesTheSessionLayer(t *testing.T) {
	backend, _, verbs := appliedBackend(t)
	if _, err := backend.Deliver("terminal-1", map[string]any{
		"verb": "resize", "pixelW": 960.0, "pixelH": 720.0, "scale": 2.0,
	}); err != nil {
		t.Fatal(err)
	}
	if len(verbs.resizes) != 1 || verbs.resizes[0] != "tab-abc123.1:960x720@2" {
		t.Fatalf("resize did not reach the sessions: %v", verbs.resizes)
	}
}
