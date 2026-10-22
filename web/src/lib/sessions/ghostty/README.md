# Ghostty terminal renderer

`TermPane.svelte` draws terminals with this folder. It is a port of T3 Code's
browser terminal (`apps/web/src/terminal/ghostty/`, MIT, Copyright (c) 2026
T3 Tools Inc.). It runs the official `libghostty-vt` C ABI as WebAssembly and
paints a Canvas 2D frame. It is not an xterm.js compatibility layer.

- `runtime.ts` loads the WebAssembly once and holds the ABI layouts.
- `core.ts` owns one terminal handle and turns ABI state into render snapshots.
- `renderer.ts` paints backgrounds and text runs onto the canvas.
- `surface.ts` owns input, IME, selection, scrolling, sizing, links and the cursor.
- `keyCodes.ts` maps browser keys to Ghostty keys.
- `links.ts` finds URLs and paths in terminal rows (T3's `terminal-links.ts`).
- `shims.ts` replaces the few T3 app helpers that the files import.
- `vendor/` holds `ghostty-vt.wasm` and the 112-byte `ghostty-write-pty.wasm`
  trampoline that carries terminal replies back to the PTY.
- `fonts/` holds the symbols-only Nerd Font.

The wasm comes from ghostty revision `9f62873bf195e4d8a762d768a1405a5f2f7b1697`
(T3's `native/libghostty-vt/VERSION`). `LICENSE` holds the T3, libghostty-vt
and Nerd Font licenses.

## Loading

`TermPane` imports `surface.ts` dynamically, so the main bundle carries none
of this. The wasm loads when the first terminal opens. The symbol font has a
Nerd Font `unicodeRange`, so the browser fetches it only when a symbol glyph
draws. The web app's CSP needs `'wasm-unsafe-eval'` in `script-src`
(`internal/websvc/server.go`).

## Changes from upstream

The behavior changes carry a `Dyna:` comment.

- Imports point at `shims.ts` and `links.ts` instead of T3 app modules.
- Element class names are plain (`ghostty-canvas`, `ghostty-input`,
  `ghostty-scrollbar`, `ghostty-scrollbar-thumb`); `TermPane` styles them.
- `write` and `resetAndWrite` also take raw `Uint8Array` PTY bytes.
- `isApplicationCursorKeys()` is public for the phone key row.
- Unused upstream integration methods are removed: `setVisible`,
  `refreshLinkActivation`, `resendSize`, `getSelectionPosition`,
  `getSelectionEndClientRect`, and `isAtBottom`. The unused
  `GhosttySelectionPosition` type is also removed.
- The symbol font loads lazily and a late font load repaints the frame.

To update, copy the new upstream files over these, then apply the changes
above again.
