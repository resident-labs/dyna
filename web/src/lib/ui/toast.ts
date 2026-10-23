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

// TOAST_MS is how long one line stays on screen.
export const TOAST_MS = 3200

// TOAST_DETAIL_MS gives the reader more time to open Details.
export const TOAST_DETAIL_MS = 10000

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
