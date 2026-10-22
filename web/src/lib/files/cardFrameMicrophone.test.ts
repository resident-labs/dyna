import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { svelteFiles } from '../../../test/svelte-files.ts'

// The desktop host allows microphone requests by the window's page, because
// WebKitGTK names no frame (desktop/host.go hostMediaAllowed). Card frames,
// on the card origin, stay off the microphone only by the permissions policy:
// a cross-origin frame without allow="microphone" cannot call getUserMedia.
// So no iframe in the app may carry an allow attribute.
test('no iframe delegates permissions such as the microphone', () => {
  const root = new URL('../..', import.meta.url).pathname
  const frames = svelteFiles(root).flatMap((path) =>
    [...readFileSync(path, 'utf8').matchAll(/<iframe\b[^>]*>/g)].map((m) => ({ path, tag: m[0] })))
  assert.ok(frames.some((f) => f.path.endsWith('CardFrame.svelte')), 'the card frame was not found')
  for (const { path, tag } of frames) assert.doesNotMatch(tag, /\sallow\s*=|\{allow\}|\.\.\./, `${path}: ${tag}`)
})
