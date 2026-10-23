<script lang="ts">
  // The Services page: one tab per service family. Each tab shows its service's
  // state in one line with its controls, then what the service did. Results
  // that repeat fold into one row; failures keep their own.
  import { untrack } from 'svelte'
  import { api } from '../../api.ts'
  import TabHeader from '../rooms/TabHeader.svelte'
  import EntryList from './EntryList.svelte'
  import ServiceLine from './ServiceLine.svelte'
  import type { Member } from '../api/room.ts'
  import { buildRows, type ChangeRow, type Inputs } from './model.ts'
  import { buildTabs, type TabId } from './tabs.ts'
  import { allParts, emptyData, loadServiceData } from './load.ts'
  import { applyControl } from './control.ts'

  // A change read here is only what the page shows: its verdict, its number,
  // its overlaps and the watcher's last poll, so the page takes the list rows
  // the room already holds.
  type ChangeLike = Pick<ChangeRow, 'reviewAt' | 'number' | 'title' | 'verdict' | 'reviewerSession' | 'kind' | 'watching' | 'checked' | 'error'> & { overlaps?: { length: number } | number }
  let { roomId, repo = '', members = [], changes = [], onopen, onopenhealth }: {
    roomId: string
    repo?: string
    members?: Member[]
    changes?: ChangeLike[]
    onopen?: (m: Member) => void
    onopenhealth?: () => void
  } = $props()

  // Every part comes from an existing call; a failed call leaves only its own
  // row empty. The page reads again every minute, so a change made elsewhere
  // shows without a reload.
  let data = $state(emptyData)
  const reload = () => loadServiceData(api, roomId, new Date(), untrack(() => data), (d) => (data = d), undefined, repo ? () => api.hookSummary(repo) : null, repo ? (name) => api.hookRuns(repo, name) : null)
  $effect(() => {
    reload()
    const t = setInterval(reload, 60 * 1000)
    return () => clearInterval(t)
  })

  let now = $state(Date.now())
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 60 * 1000)
    return () => clearInterval(t)
  })

  // The page keeps the change fields it shows: the verdict, the overlap count,
  // the session behind it and the watcher's last poll.
  const changeRows: ChangeRow[] = $derived((changes ?? []).map((c) => ({
    reviewAt: c.reviewAt, number: c.number, title: c.title, verdict: c.verdict, reviewerSession: c.reviewerSession, overlaps: c.overlaps,
    kind: c.kind, watching: c.watching, checked: c.checked, error: c.error,
  })))

  const inputs: Inputs = $derived({
    reviewer: data.reviewer, queue: data.queue, mergePaused: data.mergePaused, history: data.history, services: data.services,
    notices: data.notices, hooks: data.hooks, runs: data.runs, settings: data.settings, budget: data.budget, accounts: data.accounts,
    usage: data.usage, voice: data.voice, changes: changeRows, testHealth: data.testHealth, laneSync: data.laneSync, repo,
  })
  const rows = $derived(buildRows(inputs))
  const tabs = $derived(buildTabs(inputs, rows, now))
  let selected = $state<TabId>('merges')
  const tab = $derived(tabs.find((t) => t.id === selected) ?? tabs[0])
  // A room can hold thousands of results, so a tab shows its newest PAGE
  // entries and loads the next page on request.
  const PAGE = 40
  let limit = $state(PAGE)
  const shown = $derived(tab ? tab.entries.slice(0, limit) : [])
  const left = $derived(tab ? tab.entries.length - shown.length : 0)
  // The first read of every part has not answered yet, so the page says it is
  // reading instead of showing a row as unreadable.
  const reading = $derived(data.pending.length === allParts.length)
  const choose = (id: TabId): void => { selected = id; limit = PAGE }

  // A control writes through its call and then reads the page again, so the
  // line shows the engine's state and not what the click hoped for. A refused
  // write throws, and the row shows the engine's message.
  async function change(id: string, value: string | boolean): Promise<void> {
    await applyControl(api, { room: roomId, repo }, id, value)
    await reload()
  }

  async function run(): Promise<void> {
    await applyControl(api, { room: roomId, repo }, 'update.run', true)
    await reload()
  }

  const openSession = (session: string): void => {
    const member = members.find((m) => m.id === session)
    if (member && onopen) onopen(member)
  }
</script>

<section class="services" aria-label="Services">
  <TabHeader title="Services" blurb="What each service is doing and what it just did." />
  <div class="page">
    <div class="strip" role="tablist" aria-label="Services">
      {#each tabs as t (t.id)}
        <button role="tab" class="tab" class:sel={t.id === selected} aria-selected={t.id === selected} onclick={() => choose(t.id)}>
          <span class="dot {t.tone}" aria-hidden="true"></span>{t.name}
        </button>
      {/each}
    </div>
    {#if reading}
      <p class="none">Reading services…</p>
    {:else if tab}
      <div class="panel" role="tabpanel" aria-label={tab.name}>
        <ServiceLine rows={tab.rows} status={tab.line} wrap={tab.id !== 'more'} onWrite={change} onRun={run} />
        {#if tab.id === 'ci'}
          {#if onopenhealth}<button class="health" onclick={() => onopenhealth?.()}>Open CI health</button>{/if}
        {:else if tab.id !== 'more'}
          <EntryList entries={shown} empty="Nothing here yet." onOpen={openSession} />
          {#if left > 0}
            <button class="more" onclick={() => (limit += PAGE)}>Show more · {left} left</button>
          {/if}
        {/if}
      </div>
    {/if}
  </div>
</section>

<style>
  .services { flex: 1; min-height: 0; overflow: auto; container-type: inline-size; }
  .page { max-width: 760px; padding: 0 var(--s5) var(--s5); }
  /* The tabs scroll sideways on a phone, so none wraps or shrinks. */
  .strip { display: flex; gap: var(--s1); overflow-x: auto; scrollbar-width: none; border-bottom: 1px solid var(--line-soft); margin-bottom: var(--s3); }
  .strip::-webkit-scrollbar { display: none; }
  .tab { flex: none; display: inline-flex; align-items: center; gap: 6px; padding: var(--s2) var(--s3); font-size: var(--t-ui); color: var(--muted); border-bottom: 2px solid transparent; margin-bottom: -1px; white-space: nowrap; }
  .tab:not(.sel) { transition: color var(--motion-fast) var(--ease); }
  .tab.sel { color: var(--ink); border-bottom-color: var(--iris); }
  @media (hover: hover) {
    .tab:not(.sel):hover { color: var(--ink-2); }
  }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .dot.live { background: var(--gold); }
  .dot.on { background: var(--sage); }
  .dot.off { background: var(--line); }
  .dot.error { background: var(--coral); }
  .panel { display: flex; flex-direction: column; gap: var(--s3); }
  .none, .more { color: var(--faint); font-size: var(--t-ui); }
  .more { display: block; text-align: left; padding: var(--s2) 0; }
  .health { align-self: flex-start; padding: 3px 9px; border: 1px solid var(--line); border-radius: var(--r-xs); font-size: var(--t-sm); color: var(--muted); }
  @container (max-width: 640px) {
    .page { padding: 0 var(--s3) var(--s4); }
  }
</style>
