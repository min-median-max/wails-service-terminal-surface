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

Selection forwarding uses `soksak-contract-surface` 0.0.8. The service completes the owner address
with the recorded window and pane, rejects an invalid request before the engine call, and validates
the complete versioned snapshot before returning it. It does not interpret gesture kinds or derive
selected text.

Wheel forwarding preserves point, delta unit and modifiers, then validates the engine's one-route
answer. A scrollback answer returns state only. A mouse-report or alternate-scroll answer returns
base64 input; this service decodes it and writes it exactly once through the pane's recorded PTY
unit. The Plugin, Core and render sidecar never gain a second PTY writer. Invalid requests stop
before the engine and an engine answer with two effects stops before the PTY.

Pointer forwarding follows the same ownership: the service adds the recorded window and pane,
validates phase, button, click count, point and modifiers, validates the engine's one-route result,
and writes returned input once through the same PTY path. An ignored pointer result writes nothing.

Focus forwarding carries one explicit boolean. Focus gain transfers the native first responder and
forwards `surface.focus` to the recorded engine; focus loss forwards only the engine transaction.
The contract validates the engine/hollow-block presentation answer. This service does not inspect
or replace engine cursor shape and blink state.

Every changed geometry snapshot, including an interactive divider preview, places the terminal host
at the declared frame and reports that same frame in the receipt. The service does not defer native
placement until pointer release.

## Verification

```sh
make verify
```
