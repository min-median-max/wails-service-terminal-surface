//go:build darwin

package terminalsurface

/*
#include <stdlib.h>
#include "channel_darwin.h"
*/
import "C"

import "unsafe"

// The peer half in Go: the sidecar's moves, for the loopback tests. Nothing in
// the application calls these; the render kit implements the real sidecar side.

func peerLookUp(name string) uint32 {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	return uint32(C.soksakChannelPeerLookUp(cname))
}

func peerMakeReceive() uint32 {
	return uint32(C.soksakChannelPeerMakeReceive())
}

func peerSend(port uint32, wire []byte, rights []uint32, replyReceive uint32) bool {
	var rightsPtr *C.uint32_t
	if len(rights) > 0 {
		converted := make([]C.uint32_t, len(rights))
		for i, right := range rights {
			converted[i] = C.uint32_t(right)
		}
		rightsPtr = &converted[0]
	}
	status := C.soksakChannelPeerSend(C.uint32_t(port), unsafe.Pointer(&wire[0]),
		C.size_t(len(wire)), rightsPtr, C.size_t(len(rights)), C.uint32_t(replyReceive))
	return status == C.soksakChannelStatusDone
}

func peerReceive(port uint32, timeoutMs int) []byte {
	buffer := make([]byte, 16384)
	length := C.soksakChannelPeerReceive(C.uint32_t(port), unsafe.Pointer(&buffer[0]),
		C.size_t(len(buffer)), C.int(timeoutMs))
	if length < 0 {
		return nil
	}
	return buffer[:int(length)]
}

func peerCreateSurface(width, height uint32) unsafe.Pointer {
	return C.soksakChannelPeerCreateSurface(C.uint32_t(width), C.uint32_t(height))
}

func peerSurfacePort(surface unsafe.Pointer) uint32 {
	return uint32(C.soksakChannelPeerSurfacePort(surface))
}
