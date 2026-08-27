//go:build darwin

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#include <IOSurface/IOSurfaceRef.h>
#include <mach/mach.h>
#include <pthread.h>
#include <servers/bootstrap.h>
#include "channel_darwin.h"

// The Go half; cgo exports it C-callable.
extern void goSoksakChannelMessage(uint64_t channel, const void *bytes, size_t length,
                                   const uint32_t *ports, size_t portCount);

enum { soksakChannelMaxRights = 8, soksakChannelMaxInline = 16384 };

typedef struct {
    mach_msg_header_t header;
    mach_msg_body_t body;
    mach_msg_port_descriptor_t rights[soksakChannelMaxRights];
    uint8_t inline_bytes[soksakChannelMaxInline];
} SoksakChannelPacket;

typedef struct {
    mach_port_t port;
    uint64_t channel;
} SoksakChannelLoop;

static void *soksakChannelLoopMain(void *raw) {
    SoksakChannelLoop *loop = raw;
    union {
        SoksakChannelPacket packet;
        uint8_t space[sizeof(SoksakChannelPacket) + MAX_TRAILER_SIZE];
    } incoming;
    for (;;) {
        memset(&incoming, 0, sizeof(mach_msg_header_t) + sizeof(mach_msg_body_t));
        kern_return_t code = mach_msg(&incoming.packet.header, MACH_RCV_MSG, 0, sizeof(incoming),
                                      loop->port, MACH_MSG_TIMEOUT_NONE, MACH_PORT_NULL);
        if (code != KERN_SUCCESS) {
            break; // the receive right died: soksakChannelStop or process end
        }
        uint32_t ports[soksakChannelMaxRights] = {0};
        size_t portCount = 0;
        const uint8_t *bytes = NULL;
        size_t length = 0;
        if (incoming.packet.header.msgh_bits & MACH_MSGH_BITS_COMPLEX) {
            size_t count = incoming.packet.body.msgh_descriptor_count;
            if (count > soksakChannelMaxRights) {
                mach_msg_destroy(&incoming.packet.header);
                continue;
            }
            for (size_t i = 0; i < count; i++) {
                ports[i] = incoming.packet.rights[i].name;
            }
            portCount = count;
            size_t before = sizeof(mach_msg_header_t) + sizeof(mach_msg_body_t) +
                            count * sizeof(mach_msg_port_descriptor_t);
            bytes = (const uint8_t *)&incoming.packet + before;
            length = incoming.packet.header.msgh_size - before;
        } else {
            bytes = (const uint8_t *)&incoming.packet + sizeof(mach_msg_header_t);
            length = incoming.packet.header.msgh_size - sizeof(mach_msg_header_t);
        }
        goSoksakChannelMessage(loop->channel, bytes, length, ports, portCount);
        // Rights were handed to Go by name; only the body's memory is cleaned.
    }
    free(loop);
    return NULL;
}

int soksakChannelServe(const char *name, uint64_t channel, uint32_t *outReceivePort) {
    if (name == NULL || outReceivePort == NULL) {
        return soksakChannelStatusBadName;
    }
    mach_port_t receive = MACH_PORT_NULL;
    kern_return_t code = bootstrap_check_in(bootstrap_port, name, &receive);
    if (code != KERN_SUCCESS) {
        return soksakChannelStatusCheckInFailed;
    }
    SoksakChannelLoop *loop = calloc(1, sizeof(SoksakChannelLoop));
    loop->port = receive;
    loop->channel = channel;
    pthread_t thread;
    if (pthread_create(&thread, NULL, soksakChannelLoopMain, loop) != 0) {
        mach_port_mod_refs(mach_task_self(), receive, MACH_PORT_RIGHT_RECEIVE, -1);
        free(loop);
        return soksakChannelStatusThreadFailed;
    }
    pthread_detach(thread);
    *outReceivePort = receive;
    return soksakChannelStatusDone;
}

void soksakChannelStop(uint32_t receivePort) {
    mach_port_mod_refs(mach_task_self(), receivePort, MACH_PORT_RIGHT_RECEIVE, -1);
}

static int soksakChannelSendPacket(mach_port_t to, const void *bytes, size_t length,
                                   const uint32_t *rights, size_t rightCount,
                                   uint32_t replyReceive) {
    if (length > soksakChannelMaxInline || rightCount + (replyReceive != 0) > soksakChannelMaxRights) {
        return soksakChannelStatusSendFailed;
    }
    SoksakChannelPacket packet;
    memset(&packet, 0, sizeof(packet));
    size_t descriptors = 0;
    if (replyReceive != 0) {
        packet.rights[descriptors].name = replyReceive;
        packet.rights[descriptors].disposition = MACH_MSG_TYPE_MAKE_SEND;
        packet.rights[descriptors].type = MACH_MSG_PORT_DESCRIPTOR;
        descriptors++;
    }
    for (size_t i = 0; i < rightCount; i++) {
        packet.rights[descriptors].name = rights[i];
        packet.rights[descriptors].disposition = MACH_MSG_TYPE_COPY_SEND;
        packet.rights[descriptors].type = MACH_MSG_PORT_DESCRIPTOR;
        descriptors++;
    }
    // A message without rights carries no body word: its bytes start right
    // after the header, and writing a descriptor count there would sit on
    // top of the payload.
    size_t before = sizeof(mach_msg_header_t);
    if (descriptors > 0) {
        before += sizeof(mach_msg_body_t) + descriptors * sizeof(mach_msg_port_descriptor_t);
        packet.body.msgh_descriptor_count = (mach_msg_size_t)descriptors;
    }
    uint8_t *inlineStart = (uint8_t *)&packet + before;
    memcpy(inlineStart, bytes, length);
    size_t total = (before + length + 3) & ~(size_t)3; // mach sizes are 4-byte aligned
    packet.header.msgh_bits = MACH_MSGH_BITS(MACH_MSG_TYPE_COPY_SEND, 0);
    if (descriptors > 0) {
        packet.header.msgh_bits |= MACH_MSGH_BITS_COMPLEX;
    }
    packet.header.msgh_remote_port = to;
    packet.header.msgh_size = (mach_msg_size_t)total;
    kern_return_t code = mach_msg(&packet.header, MACH_SEND_MSG, packet.header.msgh_size, 0,
                                  MACH_PORT_NULL, MACH_MSG_TIMEOUT_NONE, MACH_PORT_NULL);
    return code == KERN_SUCCESS ? soksakChannelStatusDone : soksakChannelStatusSendFailed;
}

int soksakChannelSend(uint32_t sendPort, const void *bytes, size_t length) {
    return soksakChannelSendPacket(sendPort, bytes, length, NULL, 0, 0);
}

void soksakChannelDeallocate(uint32_t port) {
    if (port != 0) {
        mach_port_deallocate(mach_task_self(), port);
    }
}

void *soksakChannelLookupSurface(uint32_t port) {
    if (port == 0) {
        return NULL;
    }
    IOSurfaceRef surface = IOSurfaceLookupFromMachPort(port);
    mach_port_deallocate(mach_task_self(), port);
    return surface; // retained; the caller releases through soksakChannelReleaseSurface
}

void soksakChannelReleaseSurface(void *surface) {
    if (surface != NULL) {
        CFRelease((IOSurfaceRef)surface);
    }
}

int soksakChannelDisplay(void *view, void *surface) {
    if (view == NULL) {
        return soksakChannelStatusNoView;
    }
    if (surface == NULL) {
        return soksakChannelStatusNoSurface;
    }
    void (^apply)(void) = ^{
        NSView *host = (__bridge NSView *)view;
        [CATransaction begin];
        [CATransaction setDisableActions:YES];
        host.layer.contents = (__bridge id)(IOSurfaceRef)surface;
        [CATransaction commit];
    };
    if ([NSThread isMainThread]) {
        apply();
    } else {
        dispatch_sync(dispatch_get_main_queue(), apply);
    }
    return soksakChannelStatusDone;
}

// ── Peer half (the sidecar's moves, used by tests) ──────────────────────────

uint32_t soksakChannelPeerLookUp(const char *name) {
    mach_port_t send = MACH_PORT_NULL;
    if (bootstrap_look_up(bootstrap_port, name, &send) != KERN_SUCCESS) {
        return 0;
    }
    return send;
}

uint32_t soksakChannelPeerMakeReceive(void) {
    mach_port_t receive = MACH_PORT_NULL;
    if (mach_port_allocate(mach_task_self(), MACH_PORT_RIGHT_RECEIVE, &receive) != KERN_SUCCESS) {
        return 0;
    }
    return receive;
}

int soksakChannelPeerSend(uint32_t sendPort, const void *bytes, size_t length,
                          const uint32_t *rights, size_t rightCount, uint32_t replyReceive) {
    return soksakChannelSendPacket(sendPort, bytes, length, rights, rightCount, replyReceive);
}

long soksakChannelPeerReceive(uint32_t receivePort, void *buffer, size_t cap, int timeoutMs) {
    union {
        SoksakChannelPacket packet;
        uint8_t space[sizeof(SoksakChannelPacket) + MAX_TRAILER_SIZE];
    } incoming;
    memset(&incoming, 0, sizeof(mach_msg_header_t) + sizeof(mach_msg_body_t));
    kern_return_t code = mach_msg(&incoming.packet.header, MACH_RCV_MSG | MACH_RCV_TIMEOUT, 0,
                                  sizeof(incoming), receivePort, (mach_msg_timeout_t)timeoutMs,
                                  MACH_PORT_NULL);
    if (code != KERN_SUCCESS) {
        return -1;
    }
    const uint8_t *bytes = (const uint8_t *)&incoming.packet + sizeof(mach_msg_header_t);
    size_t length = incoming.packet.header.msgh_size - sizeof(mach_msg_header_t);
    if (incoming.packet.header.msgh_bits & MACH_MSGH_BITS_COMPLEX) {
        size_t count = incoming.packet.body.msgh_descriptor_count;
        size_t before = sizeof(mach_msg_header_t) + sizeof(mach_msg_body_t) +
                        count * sizeof(mach_msg_port_descriptor_t);
        bytes = (const uint8_t *)&incoming.packet + before;
        length = incoming.packet.header.msgh_size - before;
    }
    if (length > cap) {
        length = cap;
    }
    memcpy(buffer, bytes, length);
    return (long)length;
}

void *soksakChannelPeerCreateSurface(uint32_t width, uint32_t height) {
    NSDictionary *properties = @{
        (__bridge NSString *)kIOSurfaceWidth : @(width),
        (__bridge NSString *)kIOSurfaceHeight : @(height),
        (__bridge NSString *)kIOSurfaceBytesPerElement : @4,
        (__bridge NSString *)kIOSurfacePixelFormat : @((uint32_t)'BGRA'),
    };
    return (void *)IOSurfaceCreate((__bridge CFDictionaryRef)properties);
}

uint32_t soksakChannelPeerSurfacePort(void *surface) {
    if (surface == NULL) {
        return 0;
    }
    return IOSurfaceCreateMachPort((IOSurfaceRef)surface);
}

int soksakChannelSurfacePNG(void *surface, void **outBytes, size_t *outLen) {
    if (surface == NULL || outBytes == NULL || outLen == NULL) {
        return soksakChannelStatusNoSurface;
    }
    IOSurfaceRef ioSurface = (IOSurfaceRef)surface;
    if (IOSurfaceLock(ioSurface, kIOSurfaceLockReadOnly, NULL) != kIOReturnSuccess) {
        return soksakChannelStatusNoSurface;
    }
    int status = soksakChannelStatusNoSurface;
    CGImageRef image = NULL;
    CGColorSpaceRef colorSpace = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    CGContextRef bitmap = CGBitmapContextCreate(
        IOSurfaceGetBaseAddress(ioSurface),
        IOSurfaceGetWidth(ioSurface), IOSurfaceGetHeight(ioSurface), 8,
        IOSurfaceGetBytesPerRow(ioSurface), colorSpace,
        kCGImageAlphaPremultipliedFirst | kCGBitmapByteOrder32Little);
    if (bitmap != NULL) {
        image = CGBitmapContextCreateImage(bitmap);
        CGContextRelease(bitmap);
    }
    CGColorSpaceRelease(colorSpace);
    IOSurfaceUnlock(ioSurface, kIOSurfaceLockReadOnly, NULL);
    if (image == NULL) {
        return status;
    }
    CFMutableDataRef data = CFDataCreateMutable(NULL, 0);
    CGImageDestinationRef destination =
        CGImageDestinationCreateWithData(data, CFSTR("public.png"), 1, NULL);
    if (destination != NULL) {
        CGImageDestinationAddImage(destination, image, NULL);
        if (CGImageDestinationFinalize(destination)) {
            size_t length = (size_t)CFDataGetLength(data);
            void *copied = malloc(length);
            if (copied != NULL) {
                memcpy(copied, CFDataGetBytePtr(data), length);
                *outBytes = copied;
                *outLen = length;
                status = soksakChannelStatusDone;
            }
        }
        CFRelease(destination);
    }
    CFRelease(data);
    CGImageRelease(image);
    return status;
}
