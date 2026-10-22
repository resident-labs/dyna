import { createServer } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { fileURLToPath } from 'node:url'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Each server gets its own cacheDir: test files run in parallel, and servers
// that share node_modules/.vite race on renaming its deps folder.
// configFile is false: loading vite.config.js bundles it on every start, which
// cost about 3 seconds per server. The Svelte plugin is the only part tests need.
// The one hook the server needs. A test's own `after` fits, and so does node:test's top-level `after` for a server
// that a whole file shares.
interface Teardown {
  after(fn: () => Promise<void>, options?: { timeout?: number }): void
}
export function createTestServer(t: Teardown) {
  const cacheDir = mkdtemp(join(tmpdir(), 'dyna-vite-'))
  const loading = new Set<Promise<unknown>>()
  const starting = cacheDir.then(async (cacheDir) => {
    const server = await createServer({
      configFile: false,
      plugins: [svelte()],
      root: fileURLToPath(new URL('..', import.meta.url)),
      cacheDir,
      server: { middlewareMode: true, hmr: false, ws: false, port: 0, watch: null },
      appType: 'custom',
      // The Svelte plugin otherwise adds client runtime entries to include.
      optimizeDeps: { noDiscovery: true, include: [], exclude: ['svelte'] },
      // Node can load the installed server runtime without Vite transforming it.
      ssr: { external: ['svelte'] },
    })
    // Vite treats ssrLoadModule entry points as source even when dependencies
    // are external. Load the runtime with Node, matching compiled components.
    const loadModule = server.ssrLoadModule.bind(server)
    server.ssrLoadModule = (url, options) => {
      const pending = url === 'svelte/server' ? import('svelte/server') : loadModule(url, options)
      loading.add(pending)
      const settled = () => { loading.delete(pending) }
      void pending.then(settled, settled)
      return pending
    }
    return server
  })
  // Register before the first await so cancellation cannot miss a late server.
  t.after(async () => {
    try {
      const server = await starting
      while (loading.size > 0) await Promise.allSettled([...loading])
      await server.close()
    } finally {
      await rm(await cacheDir, { recursive: true, force: true })
    }
  }, { timeout: 30_000 })
  return starting
}
