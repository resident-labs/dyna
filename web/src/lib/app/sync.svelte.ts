// The room repository's saved sync counts: which remotes it has, how it
// integrates, and the last Refresh or Export the owner started. lib/lanesync
// words the popover; this module keeps the counts and the requests that read
// and start them, so App.svelte only hosts them.
import { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { remoteList } from '../lanesync/sync.ts'
import { roomData } from './roomLoader.svelte.ts'
import { router } from './router.svelte.ts'
import type { SyncStatus } from '../api/lanesync.ts'

/** The page's own side of the sync counts. App binds these once. */
export interface SyncHost {
  /** The page's reload gate, so a list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`sync: ${name} is unbound`) }
let syncHost: SyncHost = {
  reload: () => missing('reload'),
}

export function bindSync(bound: SyncHost): void {
  syncHost = bound
}

const roomId = $derived(router.roomId)
const room = $derived(roomData.feed?.room ?? null)

let syncStatus = $state<SyncStatus>({}) // saved remote counts by repo path
let syncRepo = $derived(room?.repo ? syncStatus[room.repo] ?? null : null) // {remotes, integration, operation?}
let syncRemotes = $derived(remoteList(syncRepo?.remotes))
let syncNote = $state('') // the last Refresh or Export answer that was not a start

// Quiet: the server fetches on its own; this only reads the saved counts.
// A failure of this background read shows no banner: a device without
// the scope, or a busy engine, must not look like a broken room. The
// sync popover reports errors when the owner acts.
function loadSync() {
  return syncHost.reload('sync', async (current: () => boolean) => {
    try {
      const s = await api.syncStatus()
      if (current()) syncStatus = s ?? {}
    } catch { /* quiet */ }
  })
}

// Refresh and Export start in the background; the saved operation shows
// the progress. An error, such as one already running, shows in the popover.
async function syncAction(call: (roomId: string, repo: string) => Promise<unknown>) {
  syncNote = ''
  try {
    if (!room?.repo) return
    await call(roomId, room.repo)
  } catch (e) {
    syncNote = errorText(e)
  }
  loadSync()
}

/** The room's sync counts, named as the page named them when they lived there. */
export const syncView = {
  get repo() { return syncRepo },
  get remotes() { return syncRemotes },
  get note() { return syncNote },
  loadSync,
  syncAction,
}
