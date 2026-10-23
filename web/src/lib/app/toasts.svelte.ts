// The page's one toast line and the list behind it. lib/ui/toast.ts words and
// times a line; this module keeps the line the screen shows and the timers that
// end it, so App.svelte only hosts them.
import { dismissDelay, makeToast, TOAST_DETAIL_MS, TOAST_MS } from '../ui/toast.ts'
import type { Toast as ToastLine, ToastDetail, ToastInput } from '../ui/toast.ts'
import type { RoomControlSummary } from '../rooms/roomSessionControls.ts'

let toast = $state<ToastLine | null>(null)
let toastDetail = $state<ToastDetail | null>(null)
let toastDetailOpen = $state(false)
let toastTimer: ReturnType<typeof setTimeout> | undefined

// A toast reports the result of one action the user just took: a subject, one
// sentence, and at most one thing to do next. Call sites pass display names.
function say(input: ToastInput) {
  const next = makeToast(input)
  hideToast()
  toast = next
  clearTimeout(toastTimer)
  toastTimer = setTimeout(hideToast, dismissDelay(next))
}
function hideToast() {
  toast = null
  toastDetail = null
  toastDetailOpen = false
  clearTimeout(toastTimer)
}
// runToastAction closes the toast, then follows the reader's choice.
function runToastAction(run: () => void) {
  hideToast()
  run()
}
// sayLine lets the command registry report a finished command as one
// success line, as it did before toasts had a tone.
function sayLine(message: string) {
  say({ tone: 'success', subject: message })
}
// sayDetails reports a finished bulk action in one line. The per-session
// list stays behind Details, so it never pushes the members out of view.
function sayDetails(summary: RoomControlSummary) {
  say({ subject: summary.text })
  toastDetail = { title: summary.title, items: summary.details }
  clearTimeout(toastTimer)
  toastTimer = setTimeout(hideToast, TOAST_DETAIL_MS)
}
// toggleToastDetail keeps the one line on screen while its list is open.
function toggleToastDetail() {
  if (toastDetailOpen) { closeToastDetail(); return }
  toastDetailOpen = true
  clearTimeout(toastTimer)
}
// closeToastDetail leaves the one line on screen for its normal time again.
function closeToastDetail() {
  toastDetailOpen = false
  clearTimeout(toastTimer)
  toastTimer = setTimeout(hideToast, toast?.action ? dismissDelay(toast) : TOAST_MS)
}

/** The toast line and its detail list, named as the page named them when they lived there. */
export const toasts = {
  get line() { return toast },
  get detail() { return toastDetail },
  get detailOpen() { return toastDetailOpen },
  say,
  hideToast,
  runToastAction,
  sayLine,
  sayDetails,
  toggleToastDetail,
  closeToastDetail,
}

export { say, hideToast, runToastAction, sayLine, sayDetails, toggleToastDetail, closeToastDetail }
