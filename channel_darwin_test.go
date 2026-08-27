//go:build darwin

package terminalsurface

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	surfacecontract "github.com/soksak-ai/soksak-contract-surface"
)

// The committed wire fixtures decode here and re-encode to the same bytes: the
// application consumes exactly what the contract wrote down.
func TestWireFixturesRoundTripThroughTheContract(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/soksak-ai/soksak-contract-surface").Output()
	if err != nil {
		t.Fatalf("the contract module has no directory: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "testdata", "messages")
	names, err := filepath.Glob(filepath.Join(dir, "*.bin"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no wire fixtures under %s", dir)
	}
	for _, name := range names {
		wire, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		message, err := surfacecontract.Decode(wire)
		if err != nil {
			t.Fatalf("fixture %s does not decode: %v", name, err)
		}
		again, err := surfacecontract.Encode(message)
		if err != nil {
			t.Fatalf("fixture %s does not re-encode: %v", name, err)
		}
		if string(again) != string(wire) {
			t.Errorf("fixture %s re-encodes to different bytes", filepath.Base(name))
		}
	}
}

// The loopback: this test plays the render sidecar. hello carries the reply
// port, ring carries three IOSurfaces, frameReady displays, and the surface
// displayed before comes back as released on the reply port.
func TestChannelLoopbackDisplaysAndReleases(t *testing.T) {
	identifier := fmt.Sprintf("soksak-surface-test-%d", os.Getpid())
	channel, err := OpenChannel(identifier)
	if err != nil {
		t.Fatalf("the channel did not open: %v", err)
	}
	defer channel.Close()

	const pane = "tab-loop.1"
	channel.Bind(pane, "engine-test")
	frames := make(chan uint64, 8)
	refusals := make(chan string, 8)
	channel.OnFrame = func(_ string, seq uint64) { frames <- seq }
	channel.OnRefused = func(name string) { refusals <- name }

	name, err := surfacecontract.ChannelName(identifier)
	if err != nil {
		t.Fatalf("the name did not derive: %v", err)
	}
	peer := peerLookUp(name)
	if peer == 0 {
		t.Fatalf("the peer found no service under %q", name)
	}
	reply := peerMakeReceive()
	if reply == 0 {
		t.Fatalf("the peer made no reply port")
	}

	hello, err := surfacecontract.Encode(&surfacecontract.Hello{SidecarID: "engine-test"})
	if err != nil {
		t.Fatalf("hello did not encode: %v", err)
	}
	if !peerSend(peer, hello, nil, reply) {
		t.Fatalf("hello did not send")
	}

	rights := make([]uint32, 0, 3)
	for i := 0; i < 3; i++ {
		surface := peerCreateSurface(64, 64)
		if surface == nil {
			t.Fatalf("test surface %d did not allocate", i)
		}
		port := peerSurfacePort(surface)
		if port == 0 {
			t.Fatalf("test surface %d has no mach port", i)
		}
		rights = append(rights, port)
	}
	ring, err := surfacecontract.Encode(&surfacecontract.Ring{
		Pane: pane, PixelW: 64, PixelH: 64, Scale: 2, CellW: 16, CellH: 32,
	})
	if err != nil {
		t.Fatalf("ring did not encode: %v", err)
	}
	if !peerSend(peer, ring, rights, 0) {
		t.Fatalf("ring did not send")
	}

	frameReady := func(index byte, seq uint64) {
		wire, err := surfacecontract.Encode(&surfacecontract.FrameReady{
			Pane: pane, RingIndex: index, Seq: seq,
			Damage: []surfacecontract.DamageRect{{X: 0, Y: 0, W: 4, H: 2}},
		})
		if err != nil {
			t.Fatalf("frameReady did not encode: %v", err)
		}
		if !peerSend(peer, wire, nil, 0) {
			t.Fatalf("frameReady %d did not send", index)
		}
	}
	waitFrame := func(want uint64) {
		select {
		case seq := <-frames:
			if seq != want {
				t.Fatalf("frame seq %d arrived, want %d", seq, want)
			}
		case refused := <-refusals:
			t.Fatalf("the channel refused: %s", refused)
		case <-time.After(3 * time.Second):
			t.Fatalf("no frame %d within 3s", want)
		}
	}

	frameReady(0, 1)
	waitFrame(1)
	if displayed, seq, held := channel.PaneState(pane); displayed != 0 || seq != 1 || !held {
		t.Fatalf("pane state after first frame: displayed=%d seq=%d held=%v", displayed, seq, held)
	}

	frameReady(1, 2)
	waitFrame(2)
	released := peerReceive(reply, 3000)
	if released == nil {
		t.Fatalf("no released message reached the reply port")
	}
	message, err := surfacecontract.Decode(trimWire(released))
	if err != nil {
		t.Fatalf("released did not decode: %v", err)
	}
	back, isReleased := message.(*surfacecontract.Released)
	if !isReleased || back.Pane != pane || back.RingIndex != 0 {
		t.Fatalf("the sidecar got %s, want released ring 0", surfacecontract.Format(message))
	}
	if displayed, seq, _ := channel.PaneState(pane); displayed != 1 || seq != 2 {
		t.Fatalf("pane state after second frame: displayed=%d seq=%d", displayed, seq)
	}
}

// A ring for a pane nobody bound is refused by its name — the channel never
// guesses which sidecar owns a pane.
func TestRingForAnUnboundPaneIsRefused(t *testing.T) {
	identifier := fmt.Sprintf("soksak-surface-unbound-%d", os.Getpid())
	channel, err := OpenChannel(identifier)
	if err != nil {
		t.Fatalf("the channel did not open: %v", err)
	}
	defer channel.Close()
	refusals := make(chan string, 1)
	channel.OnRefused = func(name string) { refusals <- name }

	name, _ := surfacecontract.ChannelName(identifier)
	peer := peerLookUp(name)
	surface := peerCreateSurface(16, 16)
	rights := []uint32{peerSurfacePort(surface), peerSurfacePort(surface), peerSurfacePort(surface)}
	ring, _ := surfacecontract.Encode(&surfacecontract.Ring{Pane: "tab-x.9", PixelW: 16, PixelH: 16, Scale: 1, CellW: 8, CellH: 16})
	if !peerSend(peer, ring, rights, 0) {
		t.Fatalf("ring did not send")
	}
	select {
	case refused := <-refusals:
		if !strings.Contains(refused, "tab-x.9") {
			t.Fatalf("the refusal does not name the pane: %s", refused)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no refusal within 3s")
	}
}

// The parking picture reads the displayed surface's own pixels: a PNG with the
// PNG signature, and before any frame a refusal that names the pane.
func TestSnapshotReadsTheDisplayedSurface(t *testing.T) {
	identifier := fmt.Sprintf("soksak-surface-snap-%d", os.Getpid())
	channel, err := OpenChannel(identifier)
	if err != nil {
		t.Fatalf("the channel did not open: %v", err)
	}
	defer channel.Close()
	const pane = "tab-snap.1"
	channel.Bind(pane, "engine-test")
	if _, err := channel.SnapshotPNG(pane); err == nil || !strings.Contains(err.Error(), pane) {
		t.Fatalf("a pane without a frame was not refused by name: %v", err)
	}
	frames := make(chan uint64, 4)
	channel.OnFrame = func(_ string, seq uint64) { frames <- seq }
	name, _ := surfacecontract.ChannelName(identifier)
	peer := peerLookUp(name)
	reply := peerMakeReceive()
	hello, _ := surfacecontract.Encode(&surfacecontract.Hello{SidecarID: "engine-test"})
	if !peerSend(peer, hello, nil, reply) {
		t.Fatalf("hello did not send")
	}
	rights := make([]uint32, 0, 3)
	for i := 0; i < 3; i++ {
		rights = append(rights, peerSurfacePort(peerCreateSurface(32, 16)))
	}
	ring, _ := surfacecontract.Encode(&surfacecontract.Ring{Pane: pane, PixelW: 32, PixelH: 16, Scale: 1, CellW: 8, CellH: 16})
	if !peerSend(peer, ring, rights, 0) {
		t.Fatalf("ring did not send")
	}
	frame, _ := surfacecontract.Encode(&surfacecontract.FrameReady{Pane: pane, RingIndex: 2, Seq: 1})
	if !peerSend(peer, frame, nil, 0) {
		t.Fatalf("frameReady did not send")
	}
	select {
	case <-frames:
	case <-time.After(3 * time.Second):
		t.Fatalf("no frame within 3s")
	}
	png, err := channel.SnapshotPNG(pane)
	if err != nil {
		t.Fatalf("the displayed surface did not read: %v", err)
	}
	if len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Fatalf("the picture is not a PNG (%d bytes)", len(png))
	}
}
