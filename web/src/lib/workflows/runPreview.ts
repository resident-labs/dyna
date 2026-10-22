// A run's agent row shows the start of that worker's result, and the result is
// often JSON. The row reads it the way the full view does (readable.js), so the
// preview is a sentence and not a brace. A preview that a JSON value was cut
// short of stays empty: the row's own toggle opens the readable result.
import { recordLine } from '../ui/readable.js'

export function previewLine(text: string): string {
  if (!text) return ''
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    value = text
  }
  if (typeof value === 'string') return /^[[{]/.test(value.trim()) ? '' : value
  const first: unknown = Array.isArray(value) ? value[0] : value
  if (typeof first === 'string' || typeof first === 'number') return String(first)
  if (typeof first !== 'object' || first === null) return ''
  return recordLine(first) || ''
}
