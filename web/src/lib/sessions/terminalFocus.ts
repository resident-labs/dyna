// Keyboard focus rules for a terminal pane. They stay out of the component so
// a unit test covers them without a browser.

export interface TerminalFocusState {
  readonly ready: boolean
  readonly focused: boolean
}

// terminalFocusHint shows the "click to type" hint once the terminal is on
// screen and typed keys would go nowhere because it lacks keyboard focus.
export function terminalFocusHint(state: TerminalFocusState): boolean {
  return state.ready && !state.focused
}

// terminalReady says whether the pane has a terminal to click. A surface that
// failed to draw or to attach leaves nothing behind, so it gets no hint.
export function terminalReady(state: {
  readonly attached: boolean
  readonly failed: boolean
}): boolean {
  return state.attached && !state.failed
}

// terminalFocusOnOpen decides whether a pane takes keyboard focus when it
// opens. A phone pane leaves focus alone so the soft keyboard stays down,
// unless the pane asks for focus, as the sign-in terminal does.
export function terminalFocusOnOpen(autofocus: boolean | null | undefined, narrow: boolean): boolean {
  return autofocus ?? !narrow
}
