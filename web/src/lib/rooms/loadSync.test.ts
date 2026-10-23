import { test } from 'node:test'
import assert from 'node:assert/strict'
import { installSync } from '../../../test/appModules.ts'

// The saved sync counts live in lib/app/sync.svelte.ts now, so the test installs
// that module over its own state, as appRoomLoader.ts installs the room data.
type Counts = Record<string, { origin: { ahead: number; behind: number } }>
function loader(syncStatus: () => Promise<Counts>) {
  const state = {
    roomId: '',
    syncStatus: {} as Counts,
    loadError: '',
    api: { syncStatus },
    reload: (_name: string, run: (current: () => boolean) => Promise<void>) => run(() => true),
  }
  const view = installSync(state)
  return { state, load: (): Promise<void> => view.loadSync() }
}

test('a failed background sync status read shows no error banner', async () => {
  const app = loader(async () => { throw new Error('this device cannot use SyncStatus') })
  await app.load()
  assert.equal(app.state.loadError, '', 'a background status call must not raise the room banner')
  assert.deepEqual(app.state.syncStatus, {})
})

test('a successful sync status read still updates the saved counts', async () => {
  const app = loader(async () => ({ '/r': { origin: { ahead: 0, behind: 2 } } }))
  await app.load()
  assert.equal(app.state.loadError, '')
  assert.equal(app.state.syncStatus['/r']?.origin.behind, 2)
})
