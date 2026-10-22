import { readdirSync } from 'node:fs'
import { join } from 'node:path'

// Every .svelte file under a directory, in any subdirectory, as paths joined to dir.
export function svelteFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    return entry.isDirectory() ? svelteFiles(path) : path.endsWith('.svelte') ? [path] : []
  })
}
