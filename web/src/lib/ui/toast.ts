// Toast policy.
//
// USE a toast for the short-lived result of an action the user just took:
// "Copied the address", "Saved", "Closed 13 dormant sessions", or an action
// that failed. A long result stays in one line, and its list sits behind
// Details.
//
// DO NOT use a toast for:
// - system events: they are typed feed cards from the system event contract;
// - lasting states, such as a read-only engine or a sign-in that is needed:
//   show an inline banner instead;
// - form validation: show it next to the field;
// - errors that need a decision: use inline text or a dialog.
//
// A toast names the result in a subject, then explains it in one sentence. It
// never prints a raw identifier: the call site passes a display name.

// ToastTone colors the icon and the edge: a result, a failure, or a hint.
export type ToastTone = 'success' | 'error' | 'info'

// ToastAction is the one thing the reader can do next, such as Open.
export interface ToastAction {
  label: string
  run: () => void
}

// Toast is what the app shows for one finished action.
export interface Toast {
  tone: ToastTone
  // subject names the result in a few words, with no closing period.
  subject: string
  // text explains the result in one sentence; empty when the subject says enough.
  text: string
  action?: ToastAction
  // dismiss shows the close button. It stays on, unless a result must stay.
  dismiss: boolean
}

// ToastInput is what a call site passes: the tone and the dismiss button have
// defaults, so a plain result is just a subject and a sentence.
export interface ToastInput {
  tone?: ToastTone
  subject: string
  text?: string
  action?: ToastAction
  dismiss?: boolean
}

// One line of a toast's Details view. A real failure reads as an error.
export interface ToastDetailItem {
  name: string
  text: string
  error: boolean
}

// ToastDetail is the list behind the Details button.
export interface ToastDetail {
  title: string
  items: ToastDetailItem[]
}

// TOAST_MS is how long one line stays on screen.
export const TOAST_MS = 3200

// TOAST_DETAIL_MS gives the reader more time to open Details.
export const TOAST_DETAIL_MS = 10000

// TOAST_ACTION_MS gives the reader time to reach an action button.
export const TOAST_ACTION_MS = 6000

// makeToast fills the defaults, so the view always reads a complete toast.
// The subject takes no closing period, so a sentence handed over as a subject
// still reads as a heading.
export function makeToast(input: ToastInput): Toast {
  return {
    tone: input.tone ?? 'info',
    subject: input.subject.trim().replace(/\.$/, ''),
    text: (input.text ?? '').trim(),
    action: input.action,
    dismiss: input.dismiss ?? true,
  }
}

// dismissDelay is how long this toast stays before it hides itself.
export function dismissDelay(t: Toast): number {
  return t.action ? TOAST_ACTION_MS : TOAST_MS
}

// toneRole tells a screen reader how to read the toast: a failure interrupts,
// and a plain result waits for the current sentence.
export function toneRole(tone: ToastTone): 'alert' | 'status' {
  return tone === 'error' ? 'alert' : 'status'
}

// clipText keeps a value that has no short name, such as a goal sentence or a
// shell command, inside one line. It cuts at a word boundary and adds an
// ellipsis, so the reader sees the start of the real text, never a raw ID.
export function clipText(value: string, max = 70): string {
  const clean = value.replace(/\s+/g, ' ').trim()
  if (clean.length <= max) return clean
  const cut = clean.slice(0, max)
  const space = cut.lastIndexOf(' ')
  const kept = space > max / 2 ? cut.slice(0, space) : cut
  return `${kept.replace(/[\s,;:.]+$/, '')}…`
}
