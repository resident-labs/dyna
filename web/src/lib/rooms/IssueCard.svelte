<script lang="ts">
  // The card for an issue: one shared affordance for every filer, so the
  // librarian's issue, an agent's and the owner's look the same and differ only
  // in who filed them. It reads the typed payload of the post, never its text,
  // and folds per issue: filed, changed and closed are one card.
  //
  // A post stored before the typed events has no payload; the card then reads
  // its stored title line. TODO: legacy/shim remove later — remove when
  // retained room history ages out.
  import type { EventNotice, Room } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { issueEvent } from './eventPayloads.ts'
  import type { IssueState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock, short } from '../ui/format.ts'
  import { api } from '../../api.ts'

  let {
    post,
    notice = post.notice,
    surface = 'feed',
    room = undefined,
    onissue = null,
  }: {
    post: EventPost
    notice?: EventNotice
    surface?: EventSurface
    room?: Room
    onissue?: ((issue: string) => void) | null
  } = $props()

  const stateWords: Record<IssueState, string> = {
    open: 'open',
    goal: 'on a goal',
    exported: 'exported',
    closed: 'closed',
  }
  // A closed issue is done, a goal issue is in flight; both read quieter than
  // an open one.
  const stateTone: Record<IssueState, string> = {
    open: 'gold',
    goal: 'iris',
    exported: 'sage',
    closed: 'sage',
  }

  let payload = $derived(issueEvent(post))
  let legacyTitle = $derived((post.text ?? '').split('\n')[0] ?? '')
  let number = $derived(payload?.number ?? 0)
  let title = $derived(payload?.title ?? legacyTitle)
  let issueState = $derived<IssueState | ''>(payload?.state ?? '')
  let files = $derived(payload?.files ?? [])
  let open = $state(false)
  let closing = $state(false)
  let reason = $state('')
  let busy = $state(false)
  let status = $state('')

  let who = $derived.by(() => {
    const filed = payload?.filer
    if (!filed) return ''
    return short(filed, room?.name)
  })
  let issueID = $derived(payload?.issueId ?? (payload ? post.id : ''))
  let actionable = $derived(!!payload && payload.state === 'open' && issueID !== '')

  function openPanel() {
    if (issueID) onissue?.(issueID)
  }

  async function assign() {
    if (!room || !issueID || busy) return
    busy = true
    status = ''
    try {
      await api.issueGoal(room.id, issueID)
      status = 'Assigned'
    } catch (error) {
      status = `Could not assign: ${error instanceof Error ? error.message : String(error)}`
    } finally {
      busy = false
    }
  }

  async function closeIssue() {
    if (!room || !issueID || busy) return
    const text = reason.trim()
    if (!text) return
    busy = true
    status = ''
    try {
      await api.closeIssue(room.id, issueID, text)
      closing = false
      reason = ''
      status = 'Closed'
    } catch (error) {
      status = `Could not close: ${error instanceof Error ? error.message : String(error)}`
    } finally {
      busy = false
    }
  }
</script>

<div class="card {surface}" class:open data-id={post.id}>
  <button class="sum" type="button" aria-expanded={open} onclick={() => (open = !open)}>
    {#if number > 0}<span class="num">D-{number}</span>{:else}<span class="glyph" aria-hidden="true">◆</span>{/if}
    <span class="title">{title}</span>
    {#if issueState}<span class="chip {stateTone[issueState]}">{stateWords[issueState]}</span>{/if}
    {#if who}<span class="by">filed by {who}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </button>
  <div class="body">
    {#if files.length}
      <ul class="files">
        {#each files as file, i (i)}
          <li>{file.path}{file.line ? `:${file.line}` : ''}</li>
        {/each}
      </ul>
    {/if}
    {#if payload?.symbols?.length}<p class="meta">Symbols: {payload.symbols.join(', ')}</p>{/if}
    {#if payload?.cost || payload?.direction}
      <p class="meta">{payload.cost}{#if payload.cost && payload.direction}{' · '}{/if}{payload.direction}</p>
    {/if}
    {#if payload?.assignee}<p class="meta">Assigned to {short(payload.assignee, room?.name)}</p>{/if}
    {#if payload?.reason}<p class="reason">{payload.reason}</p>{/if}
    {#if payload?.closedBy?.pr || payload?.closedBy?.commit}
      <p class="meta">Closed by {#if payload.closedBy.pr}PR #{payload.closedBy.pr}{:else}commit{/if}{#if payload.closedBy.commit} at {payload.closedBy.commit.slice(0, 8)}{/if}</p>
    {/if}
    {#if actionable}
      <div class="acts">
        <button class="btn" type="button" onclick={openPanel}>Open</button>
        <button class="btn" type="button" disabled={busy || !room} onclick={assign}>Assign</button>
        {#if closing}
          <form class="closing" onsubmit={(event) => { event.preventDefault(); closeIssue() }}>
            <input bind:value={reason} placeholder="Reason for closing" aria-label="Reason for closing" />
            <button class="btn danger" type="submit" disabled={busy || !reason.trim()}>Close issue</button>
            <button class="btn quiet" type="button" onclick={() => (closing = false)}>Cancel</button>
          </form>
        {:else}
          <button class="btn" type="button" disabled={busy || !room} onclick={() => (closing = true)}>Close</button>
        {/if}
      </div>
    {/if}
    <span class="status" role="status">{status}</span>
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div>
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    border: 0;
    background: none;
    color: var(--ink-2);
    font: inherit;
    text-align: left;
  }
  .num {
    flex: none;
    padding: 1px 6px;
    border: 1px solid var(--line);
    border-radius: 5px;
    color: var(--muted);
    font: var(--t-cap) var(--mono);
  }
  .glyph { flex: none; color: var(--iris); }
  .title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .chip { flex: none; font-size: var(--t-cap); font-weight: 600; }
  .chip.gold { color: var(--gold); }
  .chip.iris { color: var(--iris); }
  .chip.sage { color: var(--sage); }
  .by { flex: none; color: var(--muted); font-size: var(--t-sm); white-space: nowrap; }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .body { padding: 0 12px 10px; }
  .files { margin: 0 0 6px; padding: 0; list-style: none; font: var(--t-sm)/1.6 var(--mono); color: var(--muted); overflow-wrap: anywhere; }
  .meta { margin: 0 0 4px; color: var(--muted); font-size: var(--t-sm); }
  .reason { margin: 0 0 6px; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .acts { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-top: 4px; }
  .btn {
    min-height: 28px;
    padding: 3px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--raise);
    color: var(--ink-2);
    font: inherit;
    font-size: var(--t-sm);
    flex: none;
  }
  .btn.danger { color: var(--coral); }
  .closing { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; width: 100%; }
  .closing input {
    flex: 1;
    min-width: 140px;
    padding: 4px 8px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--raise);
    color: var(--ink-2);
    font: inherit;
    font-size: var(--t-sm);
  }
  .status { display: block; min-height: 0; color: var(--muted); font-size: var(--t-sm); }
  .status:empty { display: none; }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    /* One row on a phone: the number, the title and the state. A tap opens
       the files and the actions. */
    .sum { min-height: 40px; padding: 6px var(--s2); }
    .by, time { display: none; }
    .body { display: none; }
    .card.open .body { display: block; }
  }
</style>
