//go:build darwin

// The Go half of the surface channel: one checked-in service name per
// installation, every engine sidecar looks it up, and every message routes by
// pane. Wire bytes are the contract's; mach calls and port lifetimes are the
// darwin unit's. This file owns only the ring bookkeeping the SPEC gives the
// application: display the most recent signaled surface, release the one
// displayed before, never release the one on screen.
package terminalsurface

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fblocks
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore -framework IOSurface -framework ImageIO -framework CoreGraphics
#include <stdlib.h>
#include "channel_darwin.h"
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"sync"
	"unsafe"

	surfacecontract "github.com/soksak-ai/soksak-contract-surface"
)

// paneRing is the application's view of one pane's ring.
type paneRing struct {
	sidecar   string
	view      unsafe.Pointer
	held      bool
	surfaces  [surfacecontract.RingSize]unsafe.Pointer
	displayed int
	seq       uint64
	pixelW    uint32
	pixelH    uint32
	cellW     float64
	cellH     float64
}

// Channel is the application half of the surface channel.
type Channel struct {
	mu      sync.Mutex
	id      uint64
	receive C.uint32_t
	replies map[string]C.uint32_t // sidecar id → reply send right
	panes   map[string]*paneRing

	// OnFrame, OnGap, OnEnded and OnRefused report what the channel saw; they
	// are called off the receive thread and must not call back into Channel.
	OnFrame   func(pane string, seq uint64)
	OnGap     func(pane string)
	OnEnded   func(pane, reason string)
	OnRefused func(name string)
}

type displayLease struct{ apply func() }

func (lease displayLease) show() {
	if lease.apply != nil {
		lease.apply()
	}
}

// holdDisplay acquires both borrowed native objects while channel.mu still proves they are bound.
// Tests replace this function to assert the lock boundary without dereferencing native pointers.
var holdDisplay = nativeDisplayLease

func nativeDisplayLease(view, surface unsafe.Pointer) displayLease {
	if view == nil || surface == nil {
		return displayLease{}
	}
	heldView := C.soksakChannelRetainView(view)
	heldSurface := C.soksakChannelRetainSurface(surface)
	if heldView == nil || heldSurface == nil {
		if heldView != nil {
			C.soksakChannelReleaseView(heldView)
		}
		if heldSurface != nil {
			C.soksakChannelReleaseSurface(heldSurface)
		}
		return displayLease{}
	}
	return displayLease{apply: func() {
		defer C.soksakChannelReleaseView(heldView)
		defer C.soksakChannelReleaseSurface(heldSurface)
		C.soksakChannelDisplay(heldView, heldSurface)
	}}
}

var channelRegistry = struct {
	sync.Mutex
	byID map[uint64]*Channel
	next uint64
}{byID: map[uint64]*Channel{}}

// OpenChannel checks in the name derived from the installation identifier and
// starts serving it. One channel serves every render sidecar.
func OpenChannel(identifier string) (*Channel, error) {
	name, err := surfacecontract.ChannelName(identifier)
	if err != nil {
		return nil, err
	}
	channelRegistry.Lock()
	channelRegistry.next++
	id := channelRegistry.next
	channel := &Channel{
		id:      id,
		replies: make(map[string]C.uint32_t),
		panes:   make(map[string]*paneRing),
	}
	channelRegistry.byID[id] = channel
	channelRegistry.Unlock()

	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var receive C.uint32_t
	if status := C.soksakChannelServe(cname, C.uint64_t(id), &receive); status != C.soksakChannelStatusDone {
		channelRegistry.Lock()
		delete(channelRegistry.byID, id)
		channelRegistry.Unlock()
		return nil, fmt.Errorf("surface channel %q was not served: status %d", name, int(status))
	}
	channel.receive = receive
	return channel, nil
}

// Bind declares which sidecar owns a pane. The pane layer knows — it sent that
// unit surface.open — and a ring for an unbound pane is refused by name.
func (channel *Channel) Bind(pane, sidecarID string) {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	ring := channel.ring(pane)
	ring.sidecar = sidecarID
}

// BindView attaches the host view whose layer shows this pane. A frame already
// displayed lands on the view immediately.
func (channel *Channel) BindView(pane string, view unsafe.Pointer) {
	channel.mu.Lock()
	ring := channel.ring(pane)
	ring.view = view
	var display displayLease
	if view != nil && ring.held && ring.displayed >= 0 {
		display = holdDisplay(view, ring.surfaces[ring.displayed])
	}
	channel.mu.Unlock()
	display.show()
}

// UnbindView detaches the host view; the ring and its surfaces stay the
// sidecar's (P14) and keep their state for the next view.
func (channel *Channel) UnbindView(pane string) {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if ring, held := channel.panes[pane]; held {
		ring.view = nil
	}
}

// PaneState answers what the channel holds for one pane, for status and tests.
func (channel *Channel) PaneState(pane string) (displayed int, seq uint64, held bool) {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	ring, bound := channel.panes[pane]
	if !bound {
		return -1, 0, false
	}
	return ring.displayed, ring.seq, ring.held
}

// SnapshotPNG reads the displayed surface's pixels as one PNG — the parking
// picture is the sidecar's own frame, never a guess. A pane that displays
// nothing yet is refused by name.
func (channel *Channel) SnapshotPNG(pane string) ([]byte, error) {
	channel.mu.Lock()
	ring, bound := channel.panes[pane]
	var surface unsafe.Pointer
	if bound && ring.held && ring.displayed >= 0 {
		surface = ring.surfaces[ring.displayed]
	}
	channel.mu.Unlock()
	if surface == nil {
		return nil, fmt.Errorf("pane %q displays no surface yet", pane)
	}
	var bytes unsafe.Pointer
	var length C.size_t
	if status := C.soksakChannelSurfacePNG(surface, &bytes, &length); status != C.soksakChannelStatusDone {
		return nil, fmt.Errorf("pane %q pixels did not encode: status %d", pane, int(status))
	}
	defer C.free(bytes)
	return C.GoBytes(bytes, C.int(length)), nil
}

// Close stops the receive loop and releases every held surface reference.
func (channel *Channel) Close() {
	channelRegistry.Lock()
	delete(channelRegistry.byID, channel.id)
	channelRegistry.Unlock()
	channel.mu.Lock()
	C.soksakChannelStop(channel.receive)
	for _, reply := range channel.replies {
		C.soksakChannelDeallocate(reply)
	}
	channel.replies = map[string]C.uint32_t{}
	for _, ring := range channel.panes {
		releaseRingLocked(ring)
	}
	channel.mu.Unlock()
}

func (channel *Channel) ring(pane string) *paneRing {
	ring, held := channel.panes[pane]
	if !held {
		ring = &paneRing{displayed: -1}
		channel.panes[pane] = ring
	}
	return ring
}

// releaseRingLocked drops the application's retained references. The surfaces
// themselves die with the ring, owned by the sidecar (P14).
func releaseRingLocked(ring *paneRing) {
	for index, surface := range ring.surfaces {
		if surface != nil {
			C.soksakChannelReleaseSurface(surface)
			ring.surfaces[index] = nil
		}
	}
	ring.held = false
	ring.displayed = -1
}

// trimWire cuts mach's 4-byte send padding down to the message's own declared
// length (header 8 bytes + payloadLen at offset 6, big-endian) so the contract
// decoder sees exactly the bytes the encoder wrote.
func trimWire(wire []byte) []byte {
	if len(wire) < 8 {
		return wire
	}
	declared := 8 + int(binary.BigEndian.Uint16(wire[6:8]))
	if declared < len(wire) {
		return wire[:declared]
	}
	return wire
}

func (channel *Channel) refuse(name string) {
	if channel.OnRefused != nil {
		channel.OnRefused(name)
	}
}

func deallocatePorts(ports []C.uint32_t) {
	for _, port := range ports {
		C.soksakChannelDeallocate(port)
	}
}

//export goSoksakChannelMessage
func goSoksakChannelMessage(id C.uint64_t, bytes unsafe.Pointer, length C.size_t,
	ports *C.uint32_t, portCount C.size_t) {
	channelRegistry.Lock()
	channel := channelRegistry.byID[uint64(id)]
	channelRegistry.Unlock()
	rights := make([]C.uint32_t, int(portCount))
	if portCount > 0 && ports != nil {
		copy(rights, unsafe.Slice((*C.uint32_t)(ports), int(portCount)))
	}
	if channel == nil {
		deallocatePorts(rights)
		return
	}
	wire := trimWire(unsafe.Slice((*byte)(bytes), int(length)))
	message, err := surfacecontract.Decode(wire)
	if err != nil {
		deallocatePorts(rights)
		channel.refuse(err.Error())
		return
	}
	if message.PortCount() != len(rights) {
		deallocatePorts(rights)
		channel.refuse(fmt.Sprintf("%s carried %d rights, wants %d",
			surfacecontract.Format(message), len(rights), message.PortCount()))
		return
	}
	channel.consume(message, rights)
}

// consume applies one decoded message. Mach sends and the display hop happen
// outside the lock: the receive thread must never wait on the main thread
// while the main thread waits on the channel.
func (channel *Channel) consume(message surfacecontract.Message, rights []C.uint32_t) {
	type send struct {
		to   C.uint32_t
		wire []byte
	}
	var sends []send
	var display displayLease
	var frame func()

	channel.mu.Lock()
	switch m := message.(type) {
	case *surfacecontract.Hello:
		if old, held := channel.replies[m.SidecarID]; held {
			C.soksakChannelDeallocate(old)
		}
		channel.replies[m.SidecarID] = rights[0]

	case *surfacecontract.Ring:
		ring, bound := channel.panes[m.Pane]
		if !bound || ring.sidecar == "" {
			deallocatePorts(rights)
			channel.mu.Unlock()
			channel.refuse(fmt.Sprintf("ring for unbound pane %q", m.Pane))
			return
		}
		reply, connected := channel.replies[ring.sidecar]
		// A new ring invalidates the old one: every old index goes back and
		// the application's references are dropped (SPEC §4).
		if ring.held && connected {
			for index := range ring.surfaces {
				wire, err := surfacecontract.Encode(&surfacecontract.Released{Pane: m.Pane, RingIndex: byte(index)})
				if err == nil {
					sends = append(sends, send{to: reply, wire: wire})
				}
			}
		}
		releaseRingLocked(ring)
		for index, right := range rights {
			surface := C.soksakChannelLookupSurface(right)
			if surface == nil {
				releaseRingLocked(ring)
				channel.mu.Unlock()
				channel.refuse(fmt.Sprintf("ring surface %d of pane %q did not resolve", index, m.Pane))
				return
			}
			ring.surfaces[index] = surface
		}
		ring.held = true
		ring.displayed = -1
		ring.pixelW, ring.pixelH = m.PixelW, m.PixelH
		ring.cellW, ring.cellH = m.CellW, m.CellH

	case *surfacecontract.FrameReady:
		ring, bound := channel.panes[m.Pane]
		if !bound || !ring.held || int(m.RingIndex) >= len(ring.surfaces) {
			deallocatePorts(rights)
			channel.mu.Unlock()
			channel.refuse(fmt.Sprintf("frame for pane %q without a ring", m.Pane))
			return
		}
		previous := ring.displayed
		ring.displayed = int(m.RingIndex)
		ring.seq = m.Seq
		display = holdDisplay(ring.view, ring.surfaces[ring.displayed])
		if previous >= 0 && previous != ring.displayed {
			if reply, connected := channel.replies[ring.sidecar]; connected {
				wire, err := surfacecontract.Encode(&surfacecontract.Released{Pane: m.Pane, RingIndex: byte(previous)})
				if err == nil {
					sends = append(sends, send{to: reply, wire: wire})
				}
			}
		}
		if channel.OnFrame != nil {
			pane, seq := m.Pane, m.Seq
			frame = func() { channel.OnFrame(pane, seq) }
		}

	case *surfacecontract.Gap:
		if channel.OnGap != nil {
			pane := m.Pane
			frame = func() { channel.OnGap(pane) }
		}

	case *surfacecontract.Ended:
		if ring, bound := channel.panes[m.Pane]; bound {
			releaseRingLocked(ring)
		}
		if channel.OnEnded != nil {
			pane, reason := m.Pane, m.Reason
			frame = func() { channel.OnEnded(pane, reason) }
		}

	default:
		deallocatePorts(rights)
		channel.mu.Unlock()
		channel.refuse(fmt.Sprintf("%s does not travel sidecar to application",
			surfacecontract.Format(message)))
		return
	}
	channel.mu.Unlock()

	for _, out := range sends {
		C.soksakChannelSend(out.to, unsafe.Pointer(&out.wire[0]), C.size_t(len(out.wire)))
	}
	display.show()
	if frame != nil {
		frame()
	}
}
