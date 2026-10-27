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

// terminalReady says whether the pane has a terminal that can take typing. A
// surface that failed to draw leaves nothing behind, and an attach that fails
// later reports it as an exit event, so either one makes the pane not ready.
export function terminalReady(state: {
  readonly attached: boolean
  readonly failed: boolean
  readonly exited: boolean
}): boolean {
  return state.attached && !state.failed && !state.exited
}

// terminalFocusOnOpen decides whether a pane takes keyboard focus when it
// opens. A phone pane leaves focus alone so the soft keyboard stays down,
// unless the pane asks for focus, as the sign-in terminal does.
export function terminalFocusOnOpen(autofocus: boolean | null | undefined, narrow: boolean): boolean {
  return autofocus ?? !narrow
}

// terminalTypeHint is the focus hint's text: a touch screen taps, a mouse clicks.
export const terminalTypeHint = (coarse: boolean): string => (coarse ? 'Tap to type' : 'Click to type')
