package terminalsurface

import (
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
	calls   []recordedCall
	answers map[string]map[string]any
	refuse  map[string]string
}

func newFakeLinks() *fakeLinks {
	return &fakeLinks{
		answers: map[string]map[string]any{
			"terminal.prepareSession": {"observerToken": "tok-1"},
			"pty.open":                {"session": float64(7)},
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
	return Links{Send: links.send}
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

// A cold pane prepares the engine's observer, opens the shell with that token,
// subscribes the engine, opens the surface with the installation identifier,
// and resizes the pty to the grid the sidecar answered.
func TestFreshStartRunsTheOpeningOrder(t *testing.T) {
	links := newFakeLinks()
	links.refuse = map[string]string{"terminal.rehydrate": "NOT_FOUND: nothing held"}
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
		t.Fatal(err)
	}
	equalSequence(t, links.sequence(), []string{
		"soksak-sidecar-terminal-alacritty:terminal.rehydrate",
		"soksak-sidecar-terminal-alacritty:terminal.prepareSession",
		"soksak-sidecar-pty:pty.open",
		"soksak-sidecar-terminal-alacritty:terminal.ensureSession",
		"soksak-sidecar-terminal-alacritty:surface.open",
		"soksak-sidecar-pty:pty.resize",
	})
	open := links.calls[2].request
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
	surface := links.calls[4].request
	if surface["identifier"] != "install-1" {
		t.Fatalf("surface.open carries no installation identifier: %v", surface)
	}
	resize := links.calls[5].request
	if resize["cols"] != uint64(100) || resize["rows"] != uint64(30) {
		t.Fatalf("pty.resize does not follow the sidecar's grid: %v", resize)
	}
}

// A warm pane skips prepare and ensure: the engine still holds the mirror and
// its displaying observer, so only the shell handle and the surface are needed.
func TestWarmStartSkipsThePreparation(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
		t.Fatal(err)
	}
	equalSequence(t, links.sequence(), []string{
		"soksak-sidecar-terminal-alacritty:terminal.rehydrate",
		"soksak-sidecar-pty:pty.open",
		"soksak-sidecar-terminal-alacritty:surface.open",
		"soksak-sidecar-pty:pty.resize",
	})
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
		if err := sessions.Start(paneSourceMap(), 1); err != nil {
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
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Start(paneSourceMap(), 2); err != nil {
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
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
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
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
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

func TestStateAnswersWhatTheChannelSaw(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("install-1", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
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
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
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

func TestASourceMissingItsShellIsRefusedByName(t *testing.T) {
	source := paneSourceMap()
	delete(source, "shell")
	sessions := NewSessions("install-1", newFakeLinks().asLinks())
	if err := sessions.Start(source, 1); err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("a missing shell was not refused by name: %v", err)
	}
}

// A declared pixel change asks the engine for the new grid and resizes the pty
// to it; the pane record keeps the answered cell counts.
func TestResizeAsksTheEngineThenResizesThePty(t *testing.T) {
	links := newFakeLinks()
	sessions := NewSessions("com.soksak.test", links.asLinks())
	if err := sessions.Start(paneSourceMap(), 1); err != nil {
		t.Fatal(err)
	}
	links.answers["surface.resize"] = map[string]any{"cols": float64(120), "rows": float64(40)}
	links.calls = nil

	if err := sessions.Resize("tab-abc123.1", 960, 720, 2); err != nil {
		t.Fatal(err)
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
