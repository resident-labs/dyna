import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Scripts the page loads by URL: the push worker, which the browser registers
// as the classic worker /sw.js, and the microphone worklet, which the call adds
// to its audio graph as /mic-worklet.js. Each source is its own file, so the
// build emits it under the name the page asks for, and the dev server serves
// the same source at that URL.
interface UrlScript {
  source: string
  file: string
}

const URL_SCRIPTS: UrlScript[] = [
  { source: 'src/sw.ts', file: 'sw.js' },
  { source: 'src/lib/voice/micWorklet.ts', file: 'mic-worklet.js' },
]

function urlScripts(): Plugin {
  let building = false
  return {
    name: 'dyna-url-scripts',
    configResolved(config) {
      building = config.command === 'build'
    },
    buildStart() {
      // The dev server runs buildStart too, but emitFile works only in a build.
      if (building) for (const script of URL_SCRIPTS) this.emitFile({ type: 'chunk', id: script.source, fileName: script.file })
    },
    configureServer(server) {
      for (const script of URL_SCRIPTS) {
        server.middlewares.use('/' + script.file, (_req, res, next) => {
          server
            .transformRequest('/' + script.source)
            .then((result) => {
              if (!result) return next()
              res.setHeader('Content-Type', 'text/javascript')
              res.setHeader('Cache-Control', 'no-cache')
              res.end(result.code)
            })
            .catch(next)
        })
      }
    },
  }
}

export default defineConfig({
  plugins: [svelte(), urlScripts()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
})
