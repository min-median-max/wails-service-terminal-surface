// The pane sessions: the application opens a shell, tells the engine sidecar
// to render it onto the surface channel, and stays the only pty writer (P3).
// The declaration's source carries everything a pane needs; cell counts are
// absent on purpose — the sidecar answers cols and rows from the pixel box.
package terminalsurface

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
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
}

// NewSessions holds the installation identifier every surface.open carries —
// the sidecar derives the channel name from it and looks the channel up.
func NewSessions(identifier string, links Links) *Sessions {
	return &Sessions{identifier: identifier, links: links, panes: make(map[string]*paneRecord)}
}

// UseBinder connects the surface channel's pane binding.
func (sessions *Sessions) UseBinder(binder paneBinder) { sessions.binder = binder }

// paneSource is the source contract of this service, flat strings only —
// SurfaceSource is map[string]string, so numbers travel as decimal strings and
// the theme travels as one JSON string.
type paneSource struct {
	window, pane       string
	ptyUnit            string
	engineUnit         string
	pixelW, pixelH     float64
	scale              float64
	fontFamily         string
	fontPt             float64
	theme              json.RawMessage
	cwd, shell         string
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
func (sessions *Sessions) Start(source map[string]string) error {
	parsed, err := parseSource(source)
	if err != nil {
		return err
	}
	record := &paneRecord{
		pane: parsed.pane, window: parsed.window,
		ptyUnit: parsed.ptyUnit, engineUnit: parsed.engineUnit,
		phase: "opening",
	}
	sessions.mu.Lock()
	if _, held := sessions.panes[parsed.pane]; held {
		sessions.mu.Unlock()
		return fmt.Errorf("pane %s already runs", parsed.pane)
	}
	sessions.panes[parsed.pane] = record
	sessions.mu.Unlock()

	warm := sessions.rehydrate(parsed)
	if !warm {
		if err := sessions.freshObserver(parsed); err != nil {
			sessions.drop(parsed.pane)
			return err
		}
	}
	if err := sessions.openAndRender(parsed, record, warm); err != nil {
		sessions.drop(parsed.pane)
		return err
	}
	return nil
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
	// The engine subscribes its prepared stream before the shell exists, so the
	// first byte the pty emits already has its reader (the kit's own order).
	if !warm && token != "" {
		if _, err := sessions.links.send(parsed.engineUnit, "terminal.ensureSession", map[string]any{
			"window": parsed.window, "pane": parsed.pane,
			"cols": placeholderCols, "rows": placeholderRows, "observerToken": token,
		}); err != nil {
			return err
		}
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

func (sessions *Sessions) drop(pane string) {
	sessions.mu.Lock()
	delete(sessions.panes, pane)
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
	sessions.drop(pane)
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
	return sessions.links.send(record.engineUnit, command, full)
}

// NoteFrame records what the channel saw; the state verb answers from here.
func (sessions *Sessions) NoteFrame(pane string, seq uint64) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if record, held := sessions.panes[pane]; held {
		record.seq = seq
	}
}

// State answers the numbers this service holds for one pane.
func (sessions *Sessions) State(pane string) (map[string]any, error) {
	record, err := sessions.record(pane)
	if err != nil {
		return nil, err
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return map[string]any{
		"phase": record.phase, "session": record.session,
		"cols": record.cols, "rows": record.rows, "sequence": record.seq,
	}, nil
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
