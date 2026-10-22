import assert from 'node:assert/strict'
import { isRecord } from '../src/lib/api/guards.ts'

// call returns the render result as unknown. Read the HTML only after body is a string.
export function renderedBody(rendered: unknown): string {
  if (!isRecord(rendered) || typeof rendered.body !== 'string') assert.fail('render did not return a body string')
  return rendered.body
}
