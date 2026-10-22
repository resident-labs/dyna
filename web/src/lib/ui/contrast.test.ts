import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const css = readFileSync(new URL('../../app.css', import.meta.url), 'utf8')
const colors: Record<string, string> = {}
for (const [, name, hex] of css.matchAll(/--([\w-]+):\s*(#[\da-f]{6})\s*;/gi)) {
  assert.ok(name !== undefined && hex !== undefined)
  colors[name] = hex
}
function colorOf(name: string): string {
  const hex = colors[name]
  assert.ok(hex !== undefined, `app.css defines no --${name} color`)
  return hex
}
// Reads one 2-digit hex channel of a color, starting at `offset`, and converts it to linear light.
function linearChannel(hex: string, offset: number): number {
  const n = parseInt(hex.slice(offset, offset + 2), 16) / 255
  return n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4
}
const luminance = (hex: string): number =>
  linearChannel(hex, 1) * 0.2126 + linearChannel(hex, 3) * 0.7152 + linearChannel(hex, 5) * 0.0722
const contrast = (a: number, b: number): number => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)

for (const surface of ['side', 'main']) {
  test(`small faint text has at least 4.5:1 contrast on ${surface}`, () => {
    const ratio = contrast(luminance(colorOf('faint')), luminance(colorOf(surface)))
    assert.ok(ratio >= 4.5, `${colorOf('faint')} on ${colorOf(surface)} is ${ratio.toFixed(2)}:1`)
  })
}
test('faint text stays quieter than muted text', () => {
  assert.ok(luminance(colorOf('faint')) < luminance(colorOf('muted')))
})
