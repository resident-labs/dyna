// Browser fixture for the Home Services card: ?mode=failing (default) shows one
// failing hook, ?mode=healthy shows only healthy rows.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/jetbrains-mono/400.css'
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import { mount } from 'svelte'
import { api } from '../src/api.ts'
import ServicesCard from '../src/lib/rooms/ServicesCard.svelte'
import { need } from '../test/need.ts'
import type { Hook, HookSummary } from '../src/lib/api/settings.ts'
import type { Room } from '../src/lib/api/room.ts'

const healthy = new URLSearchParams(location.search).get('mode') === 'healthy'
const minutes = (n: number): string => new Date(Date.now() - n * 60000).toISOString()
const hook = (name: string, label: string, result: string, ago: number, over: Partial<Hook> = {}): HookSummary => ({
  hook: { name, label, event: 'schedule', command: '', cwd: 'data', timeout: '5m0s', onFail: 'alert', scope: 'global', addedBy: 'dyna', addedAt: '', ...over },
  lastRun: { id: `${name}-1`, repo: '', name, command: '', result, logPath: '', startedAt: minutes(ago) },
})
const summaries: HookSummary[] = [
  hook('maintenance', 'Maintenance', healthy ? 'pass' : 'error', 12),
  hook('self-update', 'Update Dyna', 'pass', 3, { event: 'main-updated' }),
  hook('account-usage', 'Account usage', 'pass', 1),
  hook('check-ins', 'Parent check-ins', 'pass', 5),
]
api.globalHookSummary = () => Promise.resolve(summaries)
api.listServices = () => Promise.resolve([
  { role: 'librarian', name: 'Librarian', page: 'services', profile: 'sol', pinned: '', source: 'auto', reason: '', scope: 'global', sessions: 1, profiles: ['sol'] },
  { role: 'reviewer', name: 'PR reviewers', page: 'services', profile: 'sol', pinned: '', source: 'auto', reason: '', scope: 'room', sessions: 2, profiles: ['sol'] },
])
api.globalHookRunLog = () => Promise.resolve({ tail: 'maintenance: pruning worktrees\nerror: disk read failed on /var/lib/dyna\nexit status 1', exitCode: 1 })

const room = { id: 'room-1', name: 'dyna', repo: '/repo', archived: false } as unknown as Room
mount(ServicesCard, { target: need(document.getElementById('app'), 'app'), props: { rooms: [room], onroom: (id: string, view?: string) => { document.body.dataset.opened = `${id}:${view ?? ''}` } } })
