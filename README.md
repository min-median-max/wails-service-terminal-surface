# wails-service-terminal-surface

A sidecar-rendered terminal, as a native surface.

One `NSView` per declared surface whose layer contents are an IOSurface a render sidecar fills.
This service implements `Backend` for `wails-service-native-compositor`, receives the surface
ring over the derived bootstrap channel of `soksak-contract-surface`, forwards keyboard, IME,
mouse and scroll input, and reads pixels for parking. It holds no cell, no glyph and no atlas:
the sidecar that owns the grid owns the pixels.

## What this is not

It is not a renderer. Fonts, glyph atlases, damage tracking and the Metal pipeline live in the
render sidecar behind `soksak-spec-sidecar-surface`. Every verb in this service is either input
forwarding, layer composition or geometry reporting.

## Shape

- `backend.go` — the inventory half: which surface a declaration owns, what changed, what came back.
- `channel_darwin.go`, `terminal_darwin.h`, `terminal_darwin.m` — the driver: the bootstrap
  channel, the host view, layer contents, input capture. The Objective-C has a compilation unit
  of its own; cgo keeps only `#cgo` directives and the include.
- `native_unsupported.go` — every other target fails by name rather than leaving a blank pane.

## State ownership

The `state` delivery reads `surface.state` once from the pane's declared engine unit. Engine
fields include renderer counters and cursor shape, visibility, position and animation. This
service then adds the session phase and id, the applied grid and the channel frame sequence it
owns. Those service-owned keys take precedence if an engine returns the same key. Frame events
trigger this read; the service does not poll the engine.

## Verification

```sh
make verify
```
