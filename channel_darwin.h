// The mach half of the surface channel. Check-in, the receive loop, port
// lifetimes, IOSurface lookup and the layer hand-off live here; wire bytes are
// opaque to this unit — Go encodes and decodes them with the contract package
// (soksak-spec-sidecar-surface SPEC §2–§4).
#ifndef SOKSAK_TERMINAL_SURFACE_CHANNEL_DARWIN_H
#define SOKSAK_TERMINAL_SURFACE_CHANNEL_DARWIN_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

enum {
    soksakChannelStatusDone = 0,
    soksakChannelStatusBadName = 10,
    soksakChannelStatusCheckInFailed = 11,
    soksakChannelStatusThreadFailed = 12,
    soksakChannelStatusSendFailed = 13,
    soksakChannelStatusNoView = 14,
    soksakChannelStatusNoSurface = 15,
};

// Check the service name in (receive right) and serve its receive loop on a
// dedicated thread. Every arriving message reaches Go through
// goSoksakChannelMessage(channel, bytes, length, ports, portCount).
int soksakChannelServe(const char *name, uint64_t channel, uint32_t *outReceivePort);

// Destroys the receive right; the blocked receive fails and the thread exits.
void soksakChannelStop(uint32_t receivePort);

// Send inline bytes, no rights, to a send right this process holds.
int soksakChannelSend(uint32_t sendPort, const void *bytes, size_t length);

// Deallocate one send right this process no longer needs.
void soksakChannelDeallocate(uint32_t port);

// The IOSurface behind a received right. The right is deallocated either way;
// a non-NULL result is a retained reference for soksakChannelReleaseSurface.
void *soksakChannelLookupSurface(uint32_t port);
void soksakChannelReleaseSurface(void *surface);

// The displayed surface becomes the host view's layer contents, on the main
// thread, inside one transaction (P7, P8).
int soksakChannelDisplay(void *view, void *surface);

// ── The peer half: what a render sidecar does. Tests play the sidecar with
// these; nothing in the application calls them.
uint32_t soksakChannelPeerLookUp(const char *name);
uint32_t soksakChannelPeerMakeReceive(void);
// Sends bytes with optional rights. `replyReceive`, when nonzero, rides first
// as a made send right (hello's reply port); `rights` follow as copied send
// rights in order (a ring's surfaces).
int soksakChannelPeerSend(uint32_t sendPort, const void *bytes, size_t length,
                          const uint32_t *rights, size_t rightCount, uint32_t replyReceive);
// Receives one message's inline bytes; answers the byte count or -1 on timeout.
long soksakChannelPeerReceive(uint32_t receivePort, void *buffer, size_t cap, int timeoutMs);
void *soksakChannelPeerCreateSurface(uint32_t width, uint32_t height);
uint32_t soksakChannelPeerSurfacePort(void *surface);

#endif

// Encodes the surface's current pixels as one PNG into a malloc'd buffer the
// caller frees. The surface stays the sidecar's; this only reads.
int soksakChannelSurfacePNG(void *surface, void **outBytes, size_t *outLen);
