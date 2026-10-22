// postSelect is the one rule for which post shows its action bar. A view
// owns one selected id; each post (or PR notice) gets these handlers.
// A mouse selects on hover and clears on leave. Touch selects only on a
// tap, never on the synthetic hover a tap makes; a second tap clears it.
// Keyboard focus selects the nearest selectable part that holds the focus.

export interface PostSelection {
  get(): string
  set(id: string): void
}

// The last input: a pointer type from pointerdown, or 'keyboard'. A click
// from a mouse does not toggle, because hover already selected the post;
// focus selects only when it came from the keyboard.
let lastPointer = ''
let listening: Document | null = null
function listenForKeys(): void {
  if (typeof document === 'undefined' || listening === document) return
  listening = document
  document.addEventListener('keydown', () => { lastPointer = 'keyboard' }, true)
}

const pointerOf = (event: Event): string => {
  const type: unknown = Reflect.get(event, 'pointerType')
  return typeof type === 'string' ? type : ''
}

interface Closest {
  closest(selector: string): unknown
}
// asElement narrows an event target to something with closest(). It checks
// the shape, not instanceof Element, so it works across windows (tests).
const asElement = (target: EventTarget | null): Closest | null =>
  target !== null && typeof Reflect.get(target, 'closest') === 'function' ? { closest: (selector) => Reflect.apply(Reflect.get(target, 'closest'), target, [selector]) } : null

// Controls inside a post act on their own; a click on them never selects.
const onControl = (event: Event): boolean => !!asElement(event.target)?.closest('button, a, summary, input, textarea, select')

// ownsEvent says whether the event belongs to this part and not to a
// selectable part nested inside it, such as a PR notice in its post.
function ownsEvent(event: Event): boolean {
  const owner = asElement(event.target)?.closest('[data-select-id]')
  return !owner || owner === event.currentTarget
}

export function postHandlers(selection: PostSelection, id: string) {
  listenForKeys()
  return {
    onpointerdown(event: Event): void {
      lastPointer = pointerOf(event)
    },
    onpointerenter(event: Event): void {
      if (pointerOf(event) === 'mouse') selection.set(id)
    },
    onpointerleave(event: Event): void {
      if (pointerOf(event) === 'mouse' && selection.get() === id) selection.set('')
    },
    onclick(event: MouseEvent): void {
      if (lastPointer === 'mouse' || onControl(event)) return
      if (!ownsEvent(event)) return
      selection.set(selection.get() === id ? '' : id)
    },
    onfocusin(event: FocusEvent): void {
      if (lastPointer === 'keyboard' && ownsEvent(event)) selection.set(id)
    },
  }
}
