import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The push worker's one source is src/sw.ts. The browser registers it as the
// classic worker /sw.js, so the build emits it as that file and the dev server
// serves the same source at that URL.
const WORKER_SOURCE = 'src/sw.ts'

function pushWorker(): Plugin {
  let building = false
  return {
    name: 'dyna-push-worker',
    configResolved(config) {
      building = config.command === 'build'
    },
    buildStart() {
      // The dev server runs buildStart too, but emitFile works only in a build.
      if (building) this.emitFile({ type: 'chunk', id: WORKER_SOURCE, fileName: 'sw.js' })
    },
    configureServer(server) {
      server.middlewares.use('/sw.js', (_req, res, next) => {
        server
          .transformRequest('/' + WORKER_SOURCE)
          .then((result) => {
            if (!result) return next()
            res.setHeader('Content-Type', 'text/javascript')
            res.setHeader('Cache-Control', 'no-cache')
            res.end(result.code)
          })
          .catch(next)
      })
    },
  }
}

export default defineConfig({
  plugins: [svelte(), pushWorker()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
})
