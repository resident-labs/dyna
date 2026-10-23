<script lang="ts">
  // The Services page: a services column that filters and controls, the
  // activity timeline, and what runs next. Activity leads on a phone, where the
  // services column follows the schedule.
  import { untrack } from 'svelte'
  import { api } from '../../api.ts'
  import TabHeader from '../rooms/TabHeader.svelte'
  import Activity from './Activity.svelte'
  import ServiceList from './ServiceList.svelte'
  import type { Member } from '../api/room.ts'
  import { buildActivity, buildRows, buildSchedule, testLine, type ChangeRow, type Inputs } from './model.ts'
  import { allParts, emptyData, loadServiceData } from './load.ts'
  import { applyControl } from './control.ts'

  // A change read here is only its verdict, its number and its overlaps, so the
  // page takes the list rows the room already holds.
  type ChangeLike = Pick<ChangeRow, 'reviewAt' | 'number' | 'title' | 'verdict' | 'reviewerSession'> & { overlaps?: { length: number } | number }

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
  const reload = () => loadServiceData(api, roomId, new Date(), untrack(() => data), (d) => (data = d), undefined, repo ? () => api.hookSummary(repo) : null)
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

  // The page reads five things from a change: its verdict, its overlap count
  // and the session behind it.
  const changeRows: ChangeRow[] = $derived((changes ?? []).map((c) => ({
    reviewAt: c.reviewAt, number: c.number, title: c.title, verdict: c.verdict, reviewerSession: c.reviewerSession, overlaps: c.overlaps,
  })))

  let selected = $state('')
  const inputs: Inputs = $derived({
    reviewer: data.reviewer, queue: data.queue, mergePaused: data.mergePaused, history: data.history, services: data.services,
    notices: data.notices, hooks: data.hooks, settings: data.settings, budget: data.budget, accounts: data.accounts,
    usage: data.usage, voice: data.voice, changes: changeRows, testHealth: data.testHealth, repo,
  })
  const rows = $derived(buildRows(inputs))
  const schedule = $derived(buildSchedule(rows, now))
  const lines = $derived(buildActivity(inputs, now))
  const filtered = $derived(selected ? lines.filter((l) => l.service === selected) : lines)
  // A room can hold thousands of results, so the timeline shows its newest
  // PAGE lines and loads the next page on request. On a phone this keeps the
  // services column a short scroll away.
  const PAGE = 60
  let limit = $state(PAGE)
  const shown = $derived(filtered.slice(0, limit))
  const left = $derived(filtered.length - shown.length)
  // The first read of every part has not answered yet, so the page says it is
  // reading instead of showing a row as unreadable.
  const reading = $derived(data.pending.length === allParts.length)
  const scope = $derived(rows.find((r) => r.id === selected)?.name ?? '')
  // The one plain line above the timeline. It shows nothing when no test is
  // over the limit, or when the read did not answer.
  const health = $derived(testLine(data.testHealth))

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
  // A new filter starts from the newest page again.
  const selectRow = (id: string): void => { selected = id; limit = PAGE }
  const toggleRow = (id: string): void => { selected = selected === id ? '' : id; limit = PAGE }
</script>

<section class="services" aria-label="Services">
  <TabHeader title="Services" blurb="What each service is doing, what it just did, and what runs next." />
  <div class="page">
    <aside class="col list" aria-label="Service controls">
      <h2>Services</h2>
      {#if reading}
        <p class="reading">Reading services…</p>
      {:else}
        <ServiceList {rows} {selected} onSelect={selectRow} onWrite={change} onRun={run} />
      {/if}
    </aside>
    <section class="col mid" aria-label="Activity">
      {#if health}
        {#if onopenhealth}
          <button class="health" onclick={() => onopenhealth?.()}>Slow tests · {health}</button>
        {:else}
          <p class="health flat">Slow tests · {health}</p>
        {/if}
      {/if}
      <div class="scope">
        <h2>Activity</h2>
        {#if selected}
          <span class="showing">Showing <b>{scope}</b><button onclick={() => (selected = '')}>show all</button></span>
        {/if}
      </div>
      <Activity lines={shown} {selected} onSelect={toggleRow} onOpen={openSession} />
      {#if left > 0}
        <button class="more" onclick={() => (limit += PAGE)}>Show more · {left} left</button>
      {/if}
    </section>
    <aside class="col next" aria-label="Scheduled next">
      <h2>Scheduled next</h2>
      {#if schedule.length}
        <ul class="sched">
          {#each schedule as item (item.id)}
            <li><button onclick={() => (selected = selected === item.id ? '' : item.id)}>{item.name}</button><span>{item.when}</span></li>
          {/each}
        </ul>
      {:else}
        <p class="none">Nothing is on a timer.</p>
      {/if}
    </aside>
  </div>
</section>

<style>
  /* The page sizes its columns from its own width, because the app's side
     panels leave about 700 px in a 1280 px window. */
  .services { flex: 1; min-height: 0; overflow: auto; container-type: inline-size; }
  .page { display: grid; grid-template-columns: minmax(220px, 264px) minmax(0, 1fr) 188px; gap: 0; padding: 0 var(--s4) var(--s5); align-items: start; }
  .col { padding: 0 var(--s3); border-right: 1px solid var(--line-soft); min-height: 60vh; }
  .col.next { border-right: 0; }
  h2 { font-size: var(--t-cap); font-weight: 500; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); margin: 0 0 var(--s2); }
  .scope { display: flex; align-items: baseline; gap: var(--s4); flex-wrap: wrap; }
  .showing { font-size: var(--t-sm); color: var(--muted); margin-bottom: var(--s2); }
  .showing b { color: var(--ink-2); font-weight: 600; }
  .showing button { margin-left: var(--s2); color: var(--iris); font-size: var(--t-sm); }
  /* The test-health line is plain: no colour. It opens the CI health tab. */
  .health { display: inline-block; margin: 0 0 var(--s2); padding: 3px 9px; border: 1px dashed var(--line); border-radius: var(--r-xs); font-size: var(--t-sm); color: var(--muted); text-align: left; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .health:hover { color: var(--ink-2); }
  }
  .sched { list-style: none; margin: 0; padding: 0; font-size: var(--t-ui); }
  .sched li { display: flex; justify-content: space-between; gap: var(--s2); padding: 6px 0; border-bottom: 1px solid var(--line-soft); }
  .sched li:last-child { border-bottom: 0; }
  .sched button { color: var(--ink-2); text-align: left; }
  .sched span { color: var(--muted); font-variant-numeric: tabular-nums; white-space: nowrap; }
  .none, .reading, .more { color: var(--faint); font-size: var(--t-ui); }
  .more { display: block; width: 100%; padding: var(--s2) 0; text-align: left; }
  .reading { margin: 0 0 var(--s2) 2px; }
  /* Below a wide window the schedule sits above the activity as one strip, so
     the timeline keeps the room it needs. */
  @container (max-width: 1020px) {
    .page { grid-template-columns: minmax(200px, 240px) minmax(0, 1fr); }
    .col.next { grid-column: 1 / -1; order: -1; border-right: 0; min-height: 0; padding-bottom: var(--s3); border-bottom: 1px solid var(--line-soft); margin-bottom: var(--s3); }
    .sched { display: flex; gap: var(--s4); flex-wrap: wrap; }
    .sched li { border-bottom: 0; gap: var(--s2); padding: 0; }
  }
  /* On a phone the activity leads, then the schedule, then the controls. */
  @container (max-width: 640px) {
    .page { grid-template-columns: minmax(0, 1fr); padding: 0 var(--s3) var(--s4); }
    .col { border-right: 0; min-height: 0; padding: 0; }
    .col.mid { order: 1; }
    .col.next { order: 2; margin-top: var(--s5); }
    /* The schedule ends the page's upper half; the divider starts the controls
       at once, so no empty band sits between them. */
    .col.list { order: 3; padding-bottom: var(--s4); border-top: 1px solid var(--line-soft); padding-top: var(--s4); }
  }
</style>
