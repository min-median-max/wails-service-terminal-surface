// The pane sessions: the application opens a shell, tells the engine sidecar
// to render it onto the surface channel, and stays the only pty writer (P3).
// The declaration's source carries everything a pane needs; cell counts are
// absent on purpose — the sidecar answers cols and rows from the pixel box.
package terminalsurface

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	surfacecontract "github.com/soksak-ai/soksak-contract-surface"
)

// paneBinder is what the sessions tell the surface channel: which sidecar's
// ring a pane accepts. Nil when the host wired no channel — the sessions still
// run and the pane simply shows nothing yet.
type paneBinder interface {
	Bind(pane, sidecarID string)
}

// paneRecord is one pane the sessions hold.
type paneRecord struct {
	pane, window string
	ptyUnit      string
	engineUnit   string
	source       paneSource
	generation   uint64
	session      uint64
	cols, rows   uint64
	phase        string
	seq          uint64
	token        string
}

// Sessions opens, steers and stops panes over the injected links.
type Sessions struct {
	identifier string
	links      Links
	binder     paneBinder

	mu    sync.Mutex
	panes map[string]*paneRecord
	locks map[string]*sync.Mutex
}

// NewSessions holds the installation identifier every surface.open carries —
// the sidecar derives the channel name from it and looks the channel up.
func NewSessions(identifier string, links Links) *Sessions {
	return &Sessions{identifier: identifier, links: links, panes: make(map[string]*paneRecord), locks: make(map[string]*sync.Mutex)}
}

func (sessions *Sessions) lockPane(pane string) func() {
	sessions.mu.Lock()
	lock := sessions.locks[pane]
	if lock == nil {
		lock = &sync.Mutex{}
		sessions.locks[pane] = lock
	}
	sessions.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// UseBinder connects the surface channel's pane binding.
func (sessions *Sessions) UseBinder(binder paneBinder) { sessions.binder = binder }

// paneSource is the source contract of this service, flat strings only —
// SurfaceSource is map[string]string, so numbers travel as decimal strings and
// the theme travels as one JSON string.
type paneSource struct {
	window, pane   string
	ptyUnit        string
	engineUnit     string
	pixelW, pixelH float64
	scale          float64
	fontFamily     string
	fontPt         float64
	theme          json.RawMessage
	cwd, shell     string
}

func parseSource(source map[string]string) (paneSource, error) {
	parsed := paneSource{
		window:     source["window"],
		pane:       source["pane"],
		ptyUnit:    source["ptyUnit"],
		engineUnit: source["engineUnit"],
		fontFamily: source["fontFamily"],
		cwd:        source["cwd"],
		shell:      source["shell"],
	}
	for _, required := range []struct{ name, value string }{
		{"window", parsed.window}, {"pane", parsed.pane},
		{"ptyUnit", parsed.ptyUnit}, {"engineUnit", parsed.engineUnit},
		{"fontFamily", parsed.fontFamily}, {"shell", parsed.shell},
	} {
		if required.value == "" {
			return paneSource{}, fmt.Errorf("terminal source is missing %s", required.name)
		}
	}
	var err error
	if parsed.pixelW, err = positive(source, "pixelW"); err != nil {
		return paneSource{}, err
	}
	if parsed.pixelH, err = positive(source, "pixelH"); err != nil {
		return paneSource{}, err
	}
	if parsed.scale, err = positive(source, "scale"); err != nil {
		return paneSource{}, err
	}
	if parsed.fontPt, err = positive(source, "fontPt"); err != nil {
		return paneSource{}, err
	}
	theme := source["theme"]
	if theme == "" || !json.Valid([]byte(theme)) {
		return paneSource{}, fmt.Errorf("terminal source theme is not a JSON document")
	}
	parsed.theme = json.RawMessage(theme)
	return parsed, nil
}

func positive(source map[string]string, key string) (float64, error) {
	value, err := strconv.ParseFloat(source[key], 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("terminal source %s is not a positive number", key)
	}
	return value, nil
}

// sessionVariables is the object-form env: session variables on top of the
// daemon's inherited environment. An array would replace the whole environment
// and drop PATH and HOME — that shape never leaves this service.
func sessionVariables(pane string) map[string]any {
	variables := map[string]any{"SOKSAK_CALLER_PANE": pane}
	if dot := strings.LastIndex(pane, "."); dot > 0 {
		variables["SOKSAK_CALLER_TAB"] = pane[:dot]
	}
	return variables
}

const placeholderCols, placeholderRows = 80, 24

// Start opens one pane: warm when the engine still holds its mirror, fresh
// otherwise. The pty write and resize stay this service's alone.
func (sessions *Sessions) Start(source map[string]string, generation uint64) error {
	parsed, err := parseSource(source)
	if err != nil {
		return err
	}
	unlock := sessions.lockPane(parsed.pane)
	defer unlock()
	record := &paneRecord{
		pane: parsed.pane, window: parsed.window,
		ptyUnit: parsed.ptyUnit, engineUnit: parsed.engineUnit,
		source:     parsed,
		generation: generation, phase: "opening",
	}
	sessions.mu.Lock()
	if held := sessions.panes[parsed.pane]; held != nil && held.generation >= generation {
		sessions.mu.Unlock()
		return nil
	}
	sessions.panes[parsed.pane] = record
	sessions.mu.Unlock()

	warm := sessions.rehydrate(parsed)
	if !warm {
		if err := sessions.freshObserver(parsed); err != nil {
			sessions.drop(parsed.pane, generation)
			return err
		}
	}
	if err := sessions.openAndRender(parsed, record, warm); err != nil {
		sessions.drop(parsed.pane, generation)
		return err
	}
	return nil
}

// RestartUnit rebuilds every live declaration that depends on one replaced process. The DOM
// generation stays unchanged; process identity and declaration identity are independent.
func (sessions *Sessions) RestartUnit(unit string) error {
	sessions.mu.Lock()
	panes := make([]string, 0, len(sessions.panes))
	for pane, record := range sessions.panes {
		if record.engineUnit == unit || record.ptyUnit == unit {
			panes = append(panes, pane)
		}
	}
	sessions.mu.Unlock()
	sort.Strings(panes)

	var failures []error
	for _, pane := range panes {
		if err := sessions.restartPane(pane, unit); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (sessions *Sessions) restartPane(pane, unit string) error {
	unlock := sessions.lockPane(pane)
	defer unlock()
	record, err := sessions.record(pane)
	if err != nil {
		return nil
	}
	if record.engineUnit != unit && record.ptyUnit != unit {
		return nil
	}
	parsed := record.source
	sessions.mu.Lock()
	record.phase = "opening"
	record.token = ""
	sessions.mu.Unlock()

	warm := sessions.rehydrate(parsed)
	if !warm {
		if err := sessions.freshObserver(parsed); err != nil {
			sessions.markRestartFailed(record)
			return fmt.Errorf("restart %s after %s replacement: %w", pane, unit, err)
		}
	}
	if err := sessions.openAndRender(parsed, record, warm); err != nil {
		sessions.markRestartFailed(record)
		return fmt.Errorf("restart %s after %s replacement: %w", pane, unit, err)
	}
	return nil
}

func (sessions *Sessions) markRestartFailed(record *paneRecord) {
	sessions.mu.Lock()
	record.phase = "blocked"
	sessions.mu.Unlock()
}

// rehydrate asks the engine for its held mirror; any refusal means fresh.
func (sessions *Sessions) rehydrate(parsed paneSource) bool {
	_, err := sessions.links.send(parsed.engineUnit, "terminal.rehydrate", map[string]any{
		"window": parsed.window, "pane": parsed.pane,
	})
	return err == nil
}

// freshObserver prepares the engine's observer and hands its token to the pty,
// so the engine sees every byte from the first one (no gap on day one).
func (sessions *Sessions) freshObserver(parsed paneSource) error {
	prepared, err := sessions.links.send(parsed.engineUnit, "terminal.prepareSession", map[string]any{
		"window": parsed.window, "pane": parsed.pane,
		"cols": placeholderCols, "rows": placeholderRows,
	})
	if err != nil {
		return err
	}
	token, _ := prepared["observerToken"].(string)
	if token == "" {
		return fmt.Errorf("terminal.prepareSession answered no observer token for %s", parsed.pane)
	}
	sessions.mu.Lock()
	if record, held := sessions.panes[parsed.pane]; held {
		record.phase = "prepared"
		record.token = token
	}
	sessions.mu.Unlock()
	return nil
}

func (sessions *Sessions) openAndRender(parsed paneSource, record *paneRecord, warm bool) error {
	open := map[string]any{
		"paneId": parsed.pane, "cols": placeholderCols, "rows": placeholderRows,
		"shell": parsed.shell, "windowLabel": parsed.window,
		"env": sessionVariables(parsed.pane),
	}
	if parsed.cwd != "" {
		open["cwd"] = parsed.cwd
	}
	sessions.mu.Lock()
	token := record.token
	sessions.mu.Unlock()
	if token != "" {
		open["observerToken"] = token
	}
	opened, err := sessions.links.send(parsed.ptyUnit, "pty.open", open)
	if err != nil {
		return err
	}
	session, valid := asUint(opened["session"])
	if !valid {
		return fmt.Errorf("pty.open answered no session for %s", parsed.pane)
	}
	sessions.mu.Lock()
	record.session = session
	record.phase = "opened"
	sessions.mu.Unlock()

	// ensure follows the open: the engine's subscribe waits for the OPENED
	// frame, and that frame exists only once the pty opened the session.
	if !warm && token != "" {
		if _, err := sessions.links.send(parsed.engineUnit, "terminal.ensureSession", map[string]any{
			"window": parsed.window, "pane": parsed.pane,
			"cols": placeholderCols, "rows": placeholderRows, "observerToken": token,
		}); err != nil {
			return err
		}
	}

	// The ring the sidecar sends during surface.open must find its pane bound
	// already — a ring for an unbound pane is refused by name.
	if sessions.binder != nil {
		sessions.binder.Bind(parsed.pane, parsed.engineUnit)
	}
	surface, err := sessions.links.send(parsed.engineUnit, "surface.open", map[string]any{
		"identifier": sessions.identifier,
		"window":     parsed.window, "pane": parsed.pane,
		"pixelW": parsed.pixelW, "pixelH": parsed.pixelH, "scale": parsed.scale,
		"font":  map[string]any{"family": parsed.fontFamily, "pt": parsed.fontPt},
		"theme": json.RawMessage(parsed.theme),
		"cwd":   parsed.cwd,
	})
	if err != nil {
		return err
	}
	cols, okCols := asUint(surface["cols"])
	rows, okRows := asUint(surface["rows"])
	if !okCols || !okRows || cols == 0 || rows == 0 {
		return fmt.Errorf("surface.open answered no grid for %s", parsed.pane)
	}
	if _, err := sessions.links.send(parsed.ptyUnit, "pty.resize", map[string]any{
		"session": session, "cols": cols, "rows": rows,
	}); err != nil {
		return err
	}
	sessions.mu.Lock()
	record.cols, record.rows = cols, rows
	record.phase = "live"
	sessions.mu.Unlock()
	return nil
}

func (sessions *Sessions) drop(pane string, generation uint64) {
	sessions.mu.Lock()
	if record := sessions.panes[pane]; record != nil && record.generation == generation {
		delete(sessions.panes, pane)
	}
	sessions.mu.Unlock()
}

func (sessions *Sessions) record(pane string) (*paneRecord, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	record, held := sessions.panes[pane]
	if !held {
		return nil, fmt.Errorf("pane %s is not running", pane)
	}
	return record, nil
}

// Stop ends the render and detaches or closes the shell. There is no
// acknowledgement to flush: the surface path never attaches a byte stream, so
// nothing was ever taken (the plan's ack step guards a stream this path does
// not have).
func (sessions *Sessions) Stop(pane, intent string) error {
	unlock := sessions.lockPane(pane)
	defer unlock()
	record, err := sessions.record(pane)
	if err != nil {
		return err
	}
	if intent != "detach" && intent != "close" {
		return fmt.Errorf("stop intent %q is neither detach nor close", intent)
	}
	if _, err := sessions.links.send(record.engineUnit, "surface.close", map[string]any{
		"window": record.window, "pane": pane,
	}); err != nil {
		return err
	}
	if _, err := sessions.links.send(record.ptyUnit, "pty.detachRenderer", map[string]any{
		"session": record.session,
	}); err != nil {
		return err
	}
	if intent == "close" {
		if _, err := sessions.links.send(record.ptyUnit, "pty.close", map[string]any{
			"session": record.session,
		}); err != nil {
			return err
		}
	}
	sessions.drop(pane, record.generation)
	return nil
}

// Remove applies one compositor removal only to the declaration generation it names. A late
// removal from an older inventory can never close the surface opened by a newer declaration.
func (sessions *Sessions) Remove(pane string, generation uint64) error {
	unlock := sessions.lockPane(pane)
	defer unlock()
	record, err := sessions.record(pane)
	if err != nil || record.generation != generation {
		return nil
	}
	if _, err := sessions.links.send(record.engineUnit, "surface.close", map[string]any{
		"window": record.window, "pane": pane,
	}); err != nil {
		return err
	}
	if _, err := sessions.links.send(record.ptyUnit, "pty.detachRenderer", map[string]any{
		"session": record.session,
	}); err != nil {
		return err
	}
	sessions.drop(pane, generation)
	return nil
}

// Input is the one pty writer (P3): confirmed text only, base64 on the wire.
func (sessions *Sessions) Input(pane, data string) error {
	record, err := sessions.record(pane)
	if err != nil {
		return err
	}
	_, err = sessions.links.send(record.ptyUnit, "pty.write", map[string]any{
		"session": record.session,
		"dataB64": base64.StdEncoding.EncodeToString([]byte(data)),
	})
	return err
}

func (sessions *Sessions) writeReturnedInput(pane, dataB64, label string) (uint64, error) {
	if dataB64 == "" {
		return 0, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return 0, fmt.Errorf("%s: dataB64: %w", label, err)
	}
	if err := sessions.Input(pane, string(decoded)); err != nil {
		return 0, err
	}
	return uint64(len(decoded)), nil
}

// Read answers the viewport text from the render sidecar's own mirror.
func (sessions *Sessions) Read(pane string, lines int) (string, error) {
	record, err := sessions.record(pane)
	if err != nil {
		return "", err
	}
	request := map[string]any{"window": record.window, "pane": pane}
	if lines > 0 {
		request["lines"] = lines
	}
	answer, err := sessions.links.send(record.engineUnit, "surface.read", request)
	if err != nil {
		return "", err
	}
	text, _ := answer["text"].(string)
	return text, nil
}

// Forward hands one surface verb to the pane's engine unchanged in meaning.
func (sessions *Sessions) Forward(pane, command string, request map[string]any) (map[string]any, error) {
	record, err := sessions.record(pane)
	if err != nil {
		return nil, err
	}
	full := map[string]any{"window": record.window, "pane": pane}
	for key, value := range request {
		full[key] = value
	}
	if command == "surface.selection" {
		if _, err := surfacecontract.ValidateSelection(full); err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
	}
	if command == "surface.wheel" {
		if _, err := surfacecontract.ValidateWheel(full); err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
	}
	if command == "surface.pointer" {
		if _, err := surfacecontract.ValidatePointer(full); err != nil {
			return nil, fmt.Errorf("INVALID_PARAMS: %w", err)
		}
	}
	answer, err := sessions.links.send(record.engineUnit, command, full)
	if err != nil {
		return nil, err
	}
	if command == "surface.selection" {
		if _, err := surfacecontract.ValidateSelectionSnapshot(answer); err != nil {
			return nil, fmt.Errorf("SELECTION_STATE_INVALID: %w", err)
		}
	}
	if command == "surface.wheel" {
		result, validateErr := surfacecontract.ValidateWheelEngineResult(answer)
		if validateErr != nil {
			return nil, fmt.Errorf("WHEEL_STATE_INVALID: %w", validateErr)
		}
		dataB64 := ""
		if result.DataB64 != nil {
			dataB64 = *result.DataB64
		}
		written, writeErr := sessions.writeReturnedInput(pane, dataB64, "WHEEL_STATE_INVALID")
		if writeErr != nil {
			return nil, writeErr
		}
		return map[string]any{
			"route": string(result.Route), "offset": result.Offset,
			"historySize": result.HistorySize, "written": written,
		}, nil
	}
	if command == "surface.pointer" {
		result, validateErr := surfacecontract.ValidatePointerEngineResult(answer)
		if validateErr != nil {
			return nil, fmt.Errorf("POINTER_STATE_INVALID: %w", validateErr)
		}
		dataB64 := ""
		if result.DataB64 != nil {
			dataB64 = *result.DataB64
		}
		written, writeErr := sessions.writeReturnedInput(pane, dataB64, "POINTER_STATE_INVALID")
		if writeErr != nil {
			return nil, writeErr
		}
		return map[string]any{"route": string(result.Route), "written": written}, nil
	}
	return answer, nil
}

// NoteFrame records what the channel saw; the state verb answers from here.
// Resize asks the pane's engine for the grid its new pixel box holds and
// resizes the pty to the answer. The declaration moves the layer; this moves
// the cells — a pane resized in pixels alone still wraps at the old columns.
func (sessions *Sessions) Resize(pane string, pixelW, pixelH, scale float64) error {
	record, err := sessions.record(pane)
	if err != nil {
		return err
	}
	surface, err := sessions.links.send(record.engineUnit, "surface.resize", map[string]any{
		"window": record.window, "pane": pane,
		"pixelW": pixelW, "pixelH": pixelH, "scale": scale,
	})
	if err != nil {
		return err
	}
	cols, okCols := asUint(surface["cols"])
	rows, okRows := asUint(surface["rows"])
	if !okCols || !okRows {
		return fmt.Errorf("surface.resize answered no grid for %s", pane)
	}
	if _, err := sessions.links.send(record.ptyUnit, "pty.resize", map[string]any{
		"session": record.session, "cols": cols, "rows": rows,
	}); err != nil {
		return err
	}
	sessions.mu.Lock()
	record.cols, record.rows = cols, rows
	sessions.mu.Unlock()
	return nil
}

func (sessions *Sessions) NoteFrame(pane string, seq uint64) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if record, held := sessions.panes[pane]; held {
		record.seq = seq
	}
}

// State combines the engine's declared surface state with the session and channel state this
// service owns. Service-owned keys are written last, so an engine cannot replace their meaning.
func (sessions *Sessions) State(pane string) (map[string]any, error) {
	record, err := sessions.record(pane)
	if err != nil {
		return nil, err
	}
	sessions.mu.Lock()
	window, engineUnit := record.window, record.engineUnit
	phase, session, generation := record.phase, record.session, record.generation
	cols, rows, sequence := record.cols, record.rows, record.seq
	sessions.mu.Unlock()
	state, err := sessions.links.send(engineUnit, "surface.state", map[string]any{
		"window": window, "pane": pane,
	})
	if err != nil {
		return nil, err
	}
	combined := make(map[string]any, len(state)+5)
	for key, value := range state {
		combined[key] = value
	}
	combined["phase"], combined["session"] = phase, session
	combined["generation"] = generation
	combined["cols"], combined["rows"], combined["sequence"] = cols, rows, sequence
	return combined, nil
}

func asUint(value any) (uint64, bool) {
	switch number := value.(type) {
	case float64:
		if number < 0 {
			return 0, false
		}
		return uint64(number), true
	case int:
		if number < 0 {
			return 0, false
		}
		return uint64(number), true
	case uint64:
		return number, true
	case json.Number:
		parsed, err := number.Int64()
		if err != nil || parsed < 0 {
			return 0, false
		}
		return uint64(parsed), true
	default:
		return 0, false
	}
}
