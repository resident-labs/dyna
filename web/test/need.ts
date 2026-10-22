// Helpers for tests that read the DOM or maps, where a missing value is a failed test and
// not a case to handle.

// need returns the value, or throws when a test expects it to exist.
export function need<T>(value: T | null | undefined, what = 'value'): T {
  if (value === null || value === undefined) throw new Error(`${what} is missing`)
  return value
}

// element returns the first element that matches, or throws.
export function element<T extends Element = HTMLElement>(root: ParentNode, selector: string): T {
  return need(root.querySelector<T>(selector), selector)
}

// elements returns every element that matches.
export function elements<T extends Element = HTMLElement>(root: ParentNode, selector: string): T[] {
  return [...root.querySelectorAll<T>(selector)]
}

// textOf is the text of an element, or '' when the element is missing.
export const textOf = (e: Element | null | undefined): string => e?.textContent ?? ''
