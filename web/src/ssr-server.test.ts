import test from 'node:test'
import type { TestContext } from 'node:test'
import assert from 'node:assert/strict'
import { Server } from 'node:net'
import { access, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createTestServer } from '../test/ssr-server.ts'

function serverLifecycle(t: TestContext) {
  let teardown: (() => Promise<void>) | undefined
  let closing: Promise<void> | undefined
  const starting = createTestServer({
    after(callback, options) {
      teardown = () => closing ??= callback()
      t.after(teardown, options)
    },
  })
  return {
    starting,
    close() {
      assert.ok(teardown, 'cleanup must be registered before server setup settles')
      return teardown()
    },
  }
}

test('SSR cleanup joins asynchronous server setup before deleting its cache', { timeout: 30_000 }, async (t) => {
  const lifecycle = serverLifecycle(t)
  try {
    const closing = lifecycle.close()
    const server = await lifecycle.starting
    await closing
    await assert.rejects(access(server.config.cacheDir), { code: 'ENOENT' })
  } finally {
    await lifecycle.starting
  }
})

test('SSR cleanup waits for an evaluating module before closing the server', { timeout: 30_000 }, async (t) => {
  const lifecycle = serverLifecycle(t)
  const server = await lifecycle.starting
  const dir = await mkdtemp(join(tmpdir(), 'dyna-ssr-pending-'))
  const entered = Promise.withResolvers<void>()
  const release = Promise.withResolvers<void>()
  const key = `ssr-fixture-${dir}`
  Reflect.set(globalThis, key, { entered: entered.resolve, release: release.promise })
  const file = join(dir, 'pending.mjs')
  await writeFile(file, `const gate = Reflect.get(globalThis, ${JSON.stringify(key)})\ngate.entered()\nawait gate.release\nexport const complete = true\n`)
  const close = t.mock.method(server, 'close')
  const loading = server.ssrLoadModule(file)
  try {
    await entered.promise
    const closing = lifecycle.close()
    await Promise.resolve()
    assert.equal(close.mock.callCount(), 0, 'an active module must finish before server.close')
    await access(server.config.cacheDir)
    await access(file)
    release.resolve()
    assert.equal((await loading).complete, true)
    await closing
    assert.equal(close.mock.callCount(), 1)
    await assert.rejects(access(server.config.cacheDir), { code: 'ENOENT' })
  } finally {
    release.resolve()
    await loading
    await lifecycle.close()
    Reflect.deleteProperty(globalThis, key)
    await rm(dir, { recursive: true, force: true })
  }
})

test('SSR test servers render without opening TCP listeners', { timeout: 30_000 }, async (t) => {
  const listeners: unknown[][] = []
  const listen = Server.prototype.listen
  t.mock.method(Server.prototype, 'listen', function (this: Server, ...args: unknown[]) {
    listeners.push(args)
    return Reflect.apply(listen, this, args)
  })
  const directory = mkdtemp(join(tmpdir(), 'dyna-ssr-listener-'))
  const rendering = (async () => {
    const component = join(await directory, 'Listener.svelte')
    await writeFile(component, '<script lang="ts">let { title }: { title: string } = $props()</script><p>{title}</p>')
    const vite = await createTestServer(t)
    const client = vite.environments.client
    assert.ok(client, 'the real Vite client environment exists')
    assert.equal(client.depsOptimizer, undefined, 'SSR fixtures must not start a client dependency optimizer')
    const { render } = await vite.ssrLoadModule('svelte/server')
    const native = await import('svelte/server')
    assert.equal(render, native.render, 'SSR fixtures must reuse the installed Node server runtime')
    const { default: Listener } = await vite.ssrLoadModule(component)
    assert.match(render(Listener, { props: { title: 'No accounts' } }).body, /No accounts/)
    assert.equal(client.depsOptimizer, undefined, 'SSR rendering must not start a client dependency optimizer')
    assert.equal(listeners.length, 0, 'SSR fixtures must not bind a shared HMR port')
  })()
  t.after(async () => {
    await Promise.allSettled([rendering])
    await rm(await directory, { recursive: true, force: true })
  }, { timeout: 30_000 })
  await rendering
})

test('SSR test servers started together use different cache folders', { timeout: 30_000 }, async (t) => {
  const [a, b] = await Promise.all([createTestServer(t), createTestServer(t)])
  assert.notEqual(a.config.cacheDir, b.config.cacheDir)
})

test('SSR test servers do not load the app vite.config.js', { timeout: 30_000 }, async (t) => {
  const vite = await createTestServer(t)
  assert.equal(vite.config.configFile, undefined)
})
