<script lang="ts">
  // The room's pull requests: the merge queue on top, then the list grouped
  // by stage (listView in prlist.js). Every row is one line: a status dot
  // (its title names the state), the title, the number and author, and the
  // check count when a check is not green. Merged rows and PRs Dyna does
  // not watch start folded.
  //
  // Props:
  //   queue, groups, states: the listView answer
  //   integration  commits integrated since the last export, shown under the queue
  //   selId      the open row's changeId
  //   names      session id → name, for the author
  //   doneLabel  the merged group's name under the current filter
  //   doneOpen   the merged fold (bindable)
  //   untrackedOpen, onfold: the "Not tracked" fold and its toggle
  //   queueOpen, blockedOpen, ontoggle: the Merge queue and Blocked folds and their toggle
  //   older, loadingOlder, failedOlder, onolder: the end of the list loads older pages when it comes into view
  //   onpick     opens a row
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import LoadOlder from '../ui/LoadOlder.svelte'
  import { changeTitle } from './pr.ts'
  import { listLine, isDone, groupOpen, checksLine, queueLine, overlapChip, type Group, type MergeItem, type MergeState, type Names, type Row as ChangeRow } from './prlist.ts'
  import type { Integration } from '../api/lanesync.ts'
  import { crossfade, slide } from 'svelte/transition'
  import { flip } from 'svelte/animate'
  import { cubicOut } from 'svelte/easing'
  import { motion, BASE } from '../ui/motion.js'
  import { ago } from '../ui/format.js'

  interface Props {
    queue?: { row: ChangeRow; item: MergeItem; state: MergeState }[]
    integration?: Integration | null
    groups?: (Group & { rows: ChangeRow[] })[]
    states?: Record<string, MergeState>
    selId?: string
    names?: Names
    doneLabel?: string
    doneOpen?: boolean
    untrackedOpen?: boolean
    queueOpen?: boolean
    blockedOpen?: boolean
    ontoggle?: (id: string) => void
    older?: boolean
    loadingOlder?: boolean
    failedOlder?: boolean
    onolder?: () => void
    onfold?: () => void
    onpick: (row: ChangeRow) => void
  }
  let { queue = [], integration = null, groups = [], states = {}, selId = '', names = {}, doneLabel = 'Merged today', doneOpen = $bindable(false), untrackedOpen = false, queueOpen = true, blockedOpen = true, ontoggle = () => {}, older = false, loadingOlder = false, failedOlder = false, onolder = () => {}, onfold = () => {}, onpick }: Props = $props()

  // A PR that changes stage leaves one keyed list and enters another (a group
  // or the merge queue). One crossfade pair, keyed by changeId, carries it
  // across; flip slides the rows that stay. The pair is global, because the
  // move often creates the queue card or removes the last row's group, and
  // Svelte skips local transitions then. A row with no counterpart (the
  // first render, a fold that opens, a PR that first appears) gets a zero
  // fallback, so only real moves animate.
  const [send, receive] = crossfade({ easing: cubicOut, fallback: () => ({ duration: 0 }) })
</script>

{#snippet line(c: ChangeRow, state: MergeState | undefined)}
  <Row dense selected={c.changeId === selId} context={isDone(c)} onclick={() => onpick(c)}>
    {#snippet lead()}<span class="sdot {state?.tone ?? ''}" role="img" aria-label={state?.label} title={state?.reason ? `${state.label} · ${state.reason}` : state?.label}></span>{/snippet}
    <span class="one"><span class="t">{changeTitle(c)}</span><span class="m">{listLine(c, names)}</span></span>
    {#snippet trail()}{@const radar = overlapChip(c)}{#if radar}<Chip tone={radar.tone}>{radar.text}</Chip>{/if}{#if checksLine(c)}<span class="checks">{checksLine(c)}</span>{/if}{/snippet}
  </Row>
{/snippet}
<div class="rows">
  {#if queue.length || integration?.commits?.length}
    {@const main = queueLine(queue)}
    <div class="queue" transition:slide={{ duration: motion(BASE), easing: cubicOut }}>
      <button class="fold" aria-expanded={queueOpen} onclick={() => ontoggle('queue')}><span aria-hidden="true">{queueOpen ? '▾' : '▸'}</span> Merge queue · {queue.length}{#if main}{' · '}<span class={main.tone}>{main.text}</span>{/if}</button>
      {#if queueOpen}
        {#each queue as q (q.row.changeId)}<div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:receive|global={{ key: q.row.changeId, duration: motion(BASE) }} out:send|global={{ key: q.row.changeId, duration: motion(BASE) }}>{@render line(q.row, q.state)}</div>{/each}
        {#if integration?.commits?.length}
          <h4 class="grp">Integrated since export <span>{integration.commits.length}</span></h4>
          {#each integration.commits as c (c.sha)}
            <div class="commit"><code>{c.sha.slice(0, 7)}</code><span class="ct">{c.title}</span><span class="cm">{c.author} · {ago(c.at)}</span></div>
          {/each}
        {/if}
      {/if}
    </div>
  {/if}
  {#each groups as g (g.id)}
    {#if g.fold}
      <button class="fold" aria-expanded={untrackedOpen} onclick={onfold} title="Not tracked: Dyna does not review these">
        <span aria-hidden="true">{untrackedOpen ? '▾' : '▸'}</span> Not tracked · {g.rows.length}
      </button>
    {:else if g.foldable}
      <button class="fold" aria-expanded={blockedOpen} onclick={() => ontoggle(g.id)}><span aria-hidden="true">{blockedOpen ? '▾' : '▸'}</span> {g.label} · {g.rows.length}</button>
    {:else if g.collapsed}
      <button class="fold" aria-expanded={doneOpen} onclick={() => (doneOpen = !doneOpen)}><span aria-hidden="true">{doneOpen ? '▾' : '▸'}</span> {doneLabel} · {g.rows.length}</button>
    {:else}
      <h4 class="grp">{g.label} <span>{g.rows.length}</span></h4>
    {/if}
    {#if groupOpen(g, { doneOpen, untrackedOpen, blockedOpen })}
      {#each g.rows as c (c.changeId)}<div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:receive|global={{ key: c.changeId, duration: motion(BASE) }} out:send|global={{ key: c.changeId, duration: motion(BASE) }}>{@render line(c, states[c.changeId ?? ''])}</div>{/each}
    {/if}
  {/each}
  <LoadOlder auto {older} loading={loadingOlder} failed={failedOlder} onclick={onolder} />
</div>

<style>
  .rows { flex: 1; overflow-y: auto; padding: var(--s2) var(--s2) var(--s4); }
  .grp { margin: var(--s3) var(--s2) var(--s1); font-size: 13px; font-weight: 400; color: var(--muted); }
  .grp span { color: var(--faint); margin-left: 4px; }
  .queue { margin: 0 0 var(--s1); }
  .sdot { display: block; width: 8px; height: 8px; border-radius: 50%; background: var(--faint); }
  .sdot.sage, .sdot.done { background: var(--sage); }
  .sdot.gold, .sdot.run { background: var(--gold); }
  .sdot.coral { background: var(--coral); }
  .sdot.iris { background: var(--iris); }
  .one { display: flex; align-items: baseline; gap: var(--s2); min-width: 0; }
  .t { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--t-body); color: var(--ink); }
  .m { flex: 0 1 auto; max-width: 40%; overflow: hidden; text-overflow: ellipsis; font-size: var(--t-sm); color: var(--faint); white-space: nowrap; }
  .checks { flex: none; font-size: var(--t-cap); color: var(--coral); }
  .commit { display: flex; align-items: baseline; gap: var(--s2); padding: 5px var(--s2); min-width: 0; font-size: var(--t-ui); }
  .commit code { font: var(--t-cap) var(--mono); color: var(--iris); flex: none; }
  .ct { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cm { flex: none; font-size: var(--t-cap); color: var(--faint); }
  .fold { display: block; width: 100%; text-align: left; padding: 9px var(--s2); font-size: 13px; color: var(--muted); }
  .fold:hover { color: var(--ink); }
  .fold .coral { color: var(--coral); }
  @media (max-width: 760px) { .fold { min-height: 44px; } }
</style>
