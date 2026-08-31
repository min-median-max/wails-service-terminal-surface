package terminalsurface

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// recordedCall is one sidecar request the fake links saw.
type recordedCall struct {
	unit    string
	command string
	request map[string]any
}

// fakeLinks answers scripted replies and records every call in order.
type fakeLinks struct {
	starts  []string
	calls   []recordedCall
	answers map[string]map[string]any
	refuse  map[string]string
}

func (links *fakeLinks) start(unit string) error {
	links.starts = append(links.starts, unit)
	return nil
}

func newFakeLinks() *fakeLinks {
	return &fakeLinks{
		answers: map[string]map[string]any{
			"surface.measure":         {"cols": float64(100), "rows": float64(30), "cellW": 15.6, "cellH": 32.0},
			"terminal.prepareSession": {"observerToken": "tok-1"},
			"pty.open":                {"session": float64(7), "created": true},
			"terminal.ensureSession":  {},
			"surface.open":            {"cols": float64(100), "rows": float64(30), "cellW": 15.6, "cellH": 32.0},
			"pty.resize":              {},
			"terminal.rehydrate":      {},
		},
	}
}

func (links *fakeLinks) send(unit, command string, request map[string]any) (map[string]any, error) {
	links.calls = append(links.calls, recordedCall{unit: unit, command: command, request: request})
	if message, refused := links.refuse[command]; refused {
		return nil, refusal(message)
	}
	return links.answers[command], nil
}

type refusal string

func (r refusal) Error() string { return string(r) }

func (links *fakeLinks) asLinks() Links {
	return Links{Start: links.start, Send: links.send}
}

func (links *fakeLinks) sequence() []string {
	sequence := make([]string, 0, len(links.calls))
	for _, call := range links.calls {
		sequence = append(sequence, call.unit+":"+call.command)
	}
	return sequence
}

func paneSourceMap() map[string]string {
	theme, _ := json.Marshal(map[string]any{"fg": "#e6e6e6", "bg": "#0a0a0a"})
	return map[string]string{
		"window": "win-abc123", "pane": "tab-abc123.1",
		"ptyUnit": "soksak-sidecar-pty", "engineUnit": "soksak-sidecar-terminal-alacritty",
		"pixelW": "640", "pixelH": "480", "scale": "2",
		"fontFamily": "Menlo", "fontPt": "13",
		"theme": string(theme), "shell": "/bin/zsh", "cwd": "/Users/someone",
	}
}

func equalSequence(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sequence is %v, wants %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sequence is %v, wants %v", got, want)
		}
	}
}

// A cold pane measures before it prepares an observer or starts a shell. Every
// process-facing request carries that one measured grid, so no initial resize
// can race the shell's first prompt.
func TestFreshStartRunsTheOpeningOrder(t *testing.T) {
	links := newFakeLinks()
	links.refuse = map[string]string{"terminal.rehydrate": "NOT_FOUND: nothing held"}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	equalSequence(t, links.starts, []string{
		"soksak-sidecar-pty",
		"soksak-sidecar-terminal-alacritty",
	})
	equalSequence(t, links.sequence(), []string{
		"soksak-sidecar-terminal-alacritty:terminal.rehydrate",
		"soksak-sidecar-terminal-alacritty:surface.measure",
		"soksak-sidecar-terminal-alacritty:terminal.prepareSession",
		"soksak-sidecar-pty:pty.open",
		"soksak-sidecar-terminal-alacritty:terminal.ensureSession",
		"soksak-sidecar-terminal-alacritty:surface.open",
	})
	open := links.calls[3].request
	if open["observerToken"] != "tok-1" {
		t.Fatalf("pty.open carries no observer token: %v", open)
	}
	environment, isObject := open["env"].(map[string]any)
	if !isObject {
		t.Fatalf("pty.open env is not an object; an array would replace the daemon's environment: %T", open["env"])
	}
	if environment["SOKSAK_CALLER_PANE"] != "tab-abc123.1" || environment["SOKSAK_CALLER_TAB"] != "tab-abc123" {
		t.Fatalf("session variables are wrong: %v", environment)
	}
	for _, index := range []int{2, 3, 4} {
		request := links.calls[index].request
		if request["cols"] != uint64(100) || request["rows"] != uint64(30) {
			t.Fatalf("%s did not receive the measured initial grid: %v", links.calls[index].command, request)
		}
	}
	surface := links.calls[5].request
	if surface["identifier"] != "install-1" {
		t.Fatalf("surface.open carries no installation identifier: %v", surface)
	}
}

// A warm pane skips prepare and ensure: the engine still holds the mirror and
// its displaying observer, so only the shell handle and the surface are needed.
func TestWarmStartSkipsThePreparation(t *testing.T) {
	links := newFakeLinks()
	links.answers["pty.open"]["created"] = false
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	equalSequence(t, links.sequence(), []string{
		"soksak-sidecar-terminal-alacritty:terminal.rehydrate",
		"soksak-sidecar-terminal-alacritty:surface.measure",
		"soksak-sidecar-pty:pty.open",
		"soksak-sidecar-pty:pty.resize",
		"soksak-sidecar-terminal-alacritty:surface.open",
	})
}

func TestEngineUnitRestartReopensEveryHeldPane(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	restart, exposed := any(sessions).(interface{ RestartUnit(string) error })
	if !exposed {
		t.Fatal("Sessions does not expose the unit-restart transaction")
	}
	links.calls = nil
	links.refuse = map[string]string{"terminal.rehydrate": "NOT_FOUND: replacement process has no mirror"}
	if err := restart.RestartUnit("soksak-sidecar-terminal-alacritty"); err != nil {
		t.Fatal(err)
	}
	equalSequence(t, links.sequence(), []string{
		"soksak-sidecar-terminal-alacritty:terminal.rehydrate",
		"soksak-sidecar-terminal-alacritty:surface.measure",
		"soksak-sidecar-terminal-alacritty:terminal.prepareSession",
		"soksak-sidecar-pty:pty.open",
		"soksak-sidecar-terminal-alacritty:terminal.ensureSession",
		"soksak-sidecar-terminal-alacritty:surface.open",
	})
	state, err := sessions.State("tab-abc123.1")
	if err != nil {
		t.Fatal(err)
	}
	if state["generation"] != uint64(1) || state["phase"] != "live" {
		t.Fatalf("process restart changed the declaration identity: %v", state)
	}
}

func TestStopDetachesAndCloseAlsoClosesTheShell(t *testing.T) {
	for _, run := range []struct {
		intent string
		tail   []string
	}{
		{"detach", []string{
			"soksak-sidecar-terminal-alacritty:surface.close",
			"soksak-sidecar-pty:pty.detachRenderer",
		}},
		{"close", []string{
			"soksak-sidecar-terminal-alacritty:surface.close",
			"soksak-sidecar-pty:pty.detachRenderer",
			"soksak-sidecar-pty:pty.close",
		}},
	} {
		links := newFakeLinks()
		sessions := NewSessions("install-1", links.asLinks())
		if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
			t.Fatal(err)
		}
		opened := len(links.calls)
		if err := sessions.Stop("tab-abc123.1", run.intent); err != nil {
			t.Fatal(err)
		}
		equalSequence(t, links.sequence()[opened:], run.tail)
		if err := sessions.Stop("tab-abc123.1", run.intent); err == nil {
			t.Fatal("a stopped pane was stopped again")
		}
	}
}

func TestLateRemovalCannotCloseANewerDeclarationGeneration(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Start(paneSourceMap(), 2, 2); err != nil {
		t.Fatal(err)
	}
	before := len(links.calls)
	if err := sessions.Remove("tab-abc123.1", 1); err != nil {
		t.Fatal(err)
	}
	if got := links.sequence()[before:]; len(got) != 0 {
		t.Fatalf("stale generation closed the current surface: %v", got)
	}
	state, err := sessions.State("tab-abc123.1")
	if err != nil {
		t.Fatal(err)
	}
	if state["generation"] != uint64(2) || state["phase"] != "live" {
		t.Fatalf("new declaration is not current: %v", state)
	}
}

func TestInputWritesThePtyAndOnlyThePty(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	before := len(links.calls)
	if err := sessions.Input("tab-abc123.1", "ls\r"); err != nil {
		t.Fatal(err)
	}
	written := links.calls[before:]
	if len(written) != 1 || written[0].command != "pty.write" || written[0].unit != "soksak-sidecar-pty" {
		t.Fatalf("input did not write the pty exactly once: %v", written)
	}
	if written[0].request["dataB64"] != "bHMN" {
		t.Fatalf("input bytes are not base64: %v", written[0].request)
	}
}

func TestReadAndForwardReachTheEngineWithTheKey(t *testing.T) {
	links := newFakeLinks()
	links.answers["surface.read"] = map[string]any{"text": "hi\n"}
	links.answers["surface.scroll"] = map[string]any{"offset": float64(3), "historySize": float64(40)}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	text, err := sessions.Read("tab-abc123.1", 5)
	if err != nil || text != "hi\n" {
		t.Fatalf("read answered %q, %v", text, err)
	}
	answer, err := sessions.Forward("tab-abc123.1", "surface.scroll", map[string]any{"lines": 3})
	if err != nil || answer["offset"] != float64(3) {
		t.Fatalf("scroll answered %v, %v", answer, err)
	}
	last := links.calls[len(links.calls)-1]
	if last.request["window"] != "win-abc123" || last.request["pane"] != "tab-abc123.1" {
		t.Fatalf("the engine call carries no pane key: %v", last.request)
	}
}

func TestSelectionForwardValidatesTheCompleteOwnedRequest(t *testing.T) {
	links := newFakeLinks()
	links.answers["surface.selection"] = map[string]any{
		"active": false, "text": "", "kind": nil, "anchor": nil, "focus": nil,
		"gestureId": nil, "sequence": float64(0),
	}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	before := len(links.calls)
	if _, err := sessions.Forward("tab-abc123.1", "surface.selection", map[string]any{}); err == nil {
		t.Fatal("selection without an action was forwarded")
	}
	if len(links.calls) != before {
		t.Fatalf("invalid selection reached the engine: %v", links.calls[before:])
	}
	answer, err := sessions.Forward(
		"tab-abc123.1", "surface.selection", map[string]any{"action": "read"},
	)
	if err != nil || answer["active"] != false {
		t.Fatalf("selection read answered %v, %v", answer, err)
	}
	last := links.calls[len(links.calls)-1]
	if last.request["window"] != "win-abc123" || last.request["pane"] != "tab-abc123.1" {
		t.Fatalf("selection lost its owner address: %v", last.request)
	}
}

func TestWheelForwardValidatesTheEngineRouteAndWritesReturnedInputOnce(t *testing.T) {
	links := newFakeLinks()
	input := []byte("\x1b[<64;2;3M")
	links.answers["surface.wheel"] = map[string]any{
		"route": "mouse-report", "offset": nil, "historySize": nil,
		"dataB64": base64.StdEncoding.EncodeToString(input),
	}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	before := len(links.calls)
	answer, err := sessions.Forward("tab-abc123.1", "surface.wheel", map[string]any{
		"point":  map[string]any{"x": float64(12), "y": float64(24)},
		"deltaX": float64(0), "deltaY": float64(1), "deltaMode": "line",
		"modifiers": map[string]any{"shift": false, "alt": false, "control": false, "meta": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer["route"] != "mouse-report" || answer["written"] != uint64(len(input)) {
		t.Fatalf("wheel answer = %v", answer)
	}
	wheelCalls := links.calls[before:]
	if len(wheelCalls) != 2 || wheelCalls[0].command != "surface.wheel" || wheelCalls[1].command != "pty.write" {
		t.Fatalf("wheel did not use engine then the single PTY writer: %v", wheelCalls)
	}
	if wheelCalls[1].request["dataB64"] != base64.StdEncoding.EncodeToString(input) {
		t.Fatalf("wheel PTY bytes changed: %v", wheelCalls[1].request)
	}
}

func TestWheelForwardRejectsInvalidRequestsAndEngineEffectsBeforePtyWrite(t *testing.T) {
	links := newFakeLinks()
	links.answers["surface.wheel"] = map[string]any{
		"route": "scrollback", "offset": float64(2), "historySize": float64(20),
		"dataB64": base64.StdEncoding.EncodeToString([]byte("not-scrollback")),
	}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	before := len(links.calls)
	if _, err := sessions.Forward("tab-abc123.1", "surface.wheel", map[string]any{}); err == nil {
		t.Fatal("wheel request without device facts reached the engine")
	}
	if len(links.calls) != before {
		t.Fatalf("invalid wheel request reached a sidecar: %v", links.calls[before:])
	}
	request := map[string]any{
		"point":  map[string]any{"x": float64(12), "y": float64(24)},
		"deltaX": float64(0), "deltaY": float64(1), "deltaMode": "line",
		"modifiers": map[string]any{"shift": false, "alt": false, "control": false, "meta": false},
	}
	if _, err := sessions.Forward("tab-abc123.1", "surface.wheel", request); err == nil {
		t.Fatal("wheel engine result with two effects was accepted")
	}
	if got := links.calls[before:]; len(got) != 1 || got[0].command != "surface.wheel" {
		t.Fatalf("invalid engine effect reached the PTY: %v", got)
	}
}

func TestPointerForwardValidatesTheEngineRouteAndWritesReturnedInputOnce(t *testing.T) {
	links := newFakeLinks()
	input := []byte("\x1b[<0;2;3M")
	links.answers["surface.pointer"] = map[string]any{
		"route": "mouse-report", "dataB64": base64.StdEncoding.EncodeToString(input),
	}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	before := len(links.calls)
	answer, err := sessions.Forward("tab-abc123.1", "surface.pointer", map[string]any{
		"point": map[string]any{"x": float64(12), "y": float64(24)},
		"phase": "down", "button": "left", "clickCount": float64(1),
		"modifiers": map[string]any{"shift": false, "alt": false, "control": false, "meta": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer["route"] != "mouse-report" || answer["written"] != uint64(len(input)) {
		t.Fatalf("pointer answer = %v", answer)
	}
	calls := links.calls[before:]
	if len(calls) != 2 || calls[0].command != "surface.pointer" || calls[1].command != "pty.write" {
		t.Fatalf("pointer did not use engine then the single PTY writer: %v", calls)
	}
}

func TestStateAnswersWhatTheChannelSaw(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	sessions.NoteFrame("tab-abc123.1", 42)
	state, err := sessions.State("tab-abc123.1")
	if err != nil {
		t.Fatal(err)
	}
	if state["sequence"] != uint64(42) || state["phase"] != "live" || state["cols"] != uint64(100) {
		t.Fatalf("state answers %v", state)
	}
}

func TestStateMergesTheDeclaredEngineSurfaceState(t *testing.T) {
	links := newFakeLinks()
	links.answers["surface.state"] = map[string]any{
		"pane": "tab-abc123.1", "paints": uint64(12),
		"phase": "engine-value", "sequence": uint64(999), "cols": uint64(1),
		"cursorRow": uint64(3), "cursorColumn": uint64(7),
		"cursorVisible": true, "cursorShape": "bar", "cursorBlinking": true,
		"cursorAnimation": map[string]any{"intervalMs": uint64(750), "phase": "off"},
	}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	sessions.NoteFrame("tab-abc123.1", 42)
	before := len(links.calls)
	state, err := sessions.State("tab-abc123.1")
	if err != nil {
		t.Fatal(err)
	}
	calls := links.calls[before:]
	if len(calls) != 1 || calls[0].unit != "soksak-sidecar-terminal-alacritty" || calls[0].command != "surface.state" {
		t.Fatalf("state did not read the declared engine once: %v", calls)
	}
	if calls[0].request["window"] != "win-abc123" || calls[0].request["pane"] != "tab-abc123.1" {
		t.Fatalf("surface.state has the wrong owner key: %v", calls[0].request)
	}
	animation, ok := state["cursorAnimation"].(map[string]any)
	if state["cursorShape"] != "bar" || state["cursorBlinking"] != true || !ok || animation["phase"] != "off" {
		t.Fatalf("engine cursor state was not published: %v", state)
	}
	if state["sequence"] != uint64(42) || state["phase"] != "live" || state["cols"] != uint64(100) {
		t.Fatalf("service-owned state was not retained: %v", state)
	}
}

func TestFailedGenerationRemainsObservableByStatus(t *testing.T) {
	links := newFakeLinks()
	links.refuse = map[string]string{"surface.open": "OPEN_FAILED: replacement surface refused"}
	sessions := NewSessions("install-1", links.asLinks())
	err := sessions.Start(paneSourceMap(), 7, 7)
	if err == nil {
		t.Fatal("failed surface generation was accepted")
	}
	status := sessions.Status()
	if len(status) != 1 || status[0]["pane"] != "tab-abc123.1" || status[0]["generation"] != uint64(7) || status[0]["phase"] != "blocked" {
		t.Fatalf("failed generation status = %#v", status)
	}
	if message, _ := status[0]["lastError"].(string); !strings.Contains(message, "OPEN_FAILED") {
		t.Fatalf("failed generation error = %#v", status[0]["lastError"])
	}
}

func TestStateSeparatesDeclarationAndLifecycleGenerations(t *testing.T) {
	sessions := NewSessions("install-1", newFakeLinks().asLinks())
	if err := sessions.Start(paneSourceMap(), 7, 42); err != nil {
		t.Fatal(err)
	}
	state, err := sessions.State("tab-abc123.1")
	if err != nil {
		t.Fatal(err)
	}
	if state["generation"] != uint64(42) || state["lifecycleGeneration"] != uint64(7) {
		t.Fatalf("surface generations = %#v", state)
	}
}

func TestDependencyStartFailureRemainsObservableBeforePaneRecordExists(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", Links{
		Start: func(unit string) error { return refusal("START_REFUSED: " + unit) },
		Send:  links.send,
	})
	if err := sessions.Start(paneSourceMap(), 9, 9); err == nil {
		t.Fatal("dependency start failure was accepted")
	}
	status := sessions.Status()
	if len(status) != 1 || status[0]["pane"] != "tab-abc123.1" || status[0]["generation"] != uint64(9) || status[0]["phase"] != "blocked" {
		t.Fatalf("pre-record failure status = %#v", status)
	}
	if message, _ := status[0]["lastError"].(string); !strings.Contains(message, "START_REFUSED") {
		t.Fatalf("pre-record failure error = %#v", status[0]["lastError"])
	}
}

func TestNewerStartFailureIsReportedBesideTheStillLiveGeneration(t *testing.T) {
	sessions := NewSessions("install-1", newFakeLinks().asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	sessions.recordPreStartFailure("win-abc123", "tab-abc123.1", 2, 2, refusal("START_REFUSED"))
	status := sessions.Status()
	if len(status) != 1 || status[0]["generation"] != uint64(1) || status[0]["phase"] != "live" || status[0]["failedGeneration"] != uint64(2) {
		t.Fatalf("live generation with newer failure = %#v", status)
	}
	if message, _ := status[0]["startError"].(string); !strings.Contains(message, "START_REFUSED") {
		t.Fatalf("newer start error = %#v", status[0]["startError"])
	}
}

func TestASourceMissingItsShellIsRefusedByName(t *testing.T) {
	source := paneSourceMap()
	delete(source, "shell")
	sessions := NewSessions("install-1", newFakeLinks().asLinks())
	if err := sessions.Start(source, 1, 1); err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("a missing shell was not refused by name: %v", err)
	}
}

// A declared pixel change asks the engine for the new grid and resizes the pty
// to it; the pane record keeps the answered cell counts.
func TestResizeAsksTheEngineThenResizesThePty(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("com.soksak.test", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1, 1); err != nil {
		t.Fatal(err)
	}
	links.answers["surface.resize"] = map[string]any{"cols": float64(120), "rows": float64(40)}
	links.calls = nil

	grid, err := sessions.Resize("tab-abc123.1", 960, 720, 2)
	if err != nil {
		t.Fatal(err)
	}
	if grid != (Grid{Cols: 120, Rows: 40}) {
		t.Fatalf("resize result = %+v, want 120x40", grid)
	}

	sequence := links.sequence()
	want := []string{
		"soksak-sidecar-terminal-alacritty:surface.resize",
		"soksak-sidecar-pty:pty.resize",
	}
	if len(sequence) != len(want) || sequence[0] != want[0] || sequence[1] != want[1] {
		t.Fatalf("resize sequence = %v, want %v", sequence, want)
	}
	resize := links.calls[1].request
	if resize["cols"] != uint64(120) || resize["rows"] != uint64(40) {
		t.Fatalf("pty.resize carried %v, want the engine's 120x40", resize)
	}
}
