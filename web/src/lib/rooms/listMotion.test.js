import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createTestServer } from '../../../test/ssr-server.ts'
import { compile } from 'svelte/compiler'

const files = ['Sidebar', 'RoomRail', 'Members']

for (const file of files) {
  test(`${file} uses shared motion for keyed list movement and local arrival fades`, () => {
    const source = readFileSync(new URL(`./${file}.svelte`, import.meta.url), 'utf8')
    assert.match(source, /import \{[^}]*motion[^}]*\} from '\.\.\/ui\/motion\.js'/)
    assert.match(source, /animate:flip=\{\{ duration: motion\(BASE\), easing: cubicOut \}\}/)
    assert.match(source, /in:fade=\{\{ duration: motion\(FAST\) \}\}/)
    assert.doesNotMatch(source, /(?:in|out):fade\|global/)
    assert.equal([...source.matchAll(/animate:flip=/g)].length, { Sidebar: 4, RoomRail: 2, Members: 3 }[file])
    assert.doesNotThrow(() => compile(source, { filename: `${file}.svelte`, generate: 'client' }))
    if (file !== 'Sidebar') assert.doesNotMatch(source, /out:fade/, 'filter and fold changes remove rows immediately')
    else assert.equal([...source.matchAll(/out:fade=/g)].length, 3, 'only active lists fade removals')
  })
}

