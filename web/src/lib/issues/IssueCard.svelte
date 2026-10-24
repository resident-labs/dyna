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
  import type { EventPost, EventSurface } from '../rooms/eventRenderers.ts'
  import { issueEvent } from '../rooms/eventPayloads.ts'
  import type { IssueState } from '../rooms/events.gen.ts'
  import EventDetails from '../rooms/EventDetails.svelte'
  import { short } from '../ui/format.ts'
  import EventRow from '../rooms/EventRow.svelte'
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
  // What closed the issue, named once. An automatic close carries the merging
  // PR and its commit, so the reason line stays out: it says the same fact.
  let closedLine = $derived.by(() => {
    const origin = payload?.closedBy
    if (!origin?.pr && !origin?.commit) return ''
    const what = origin?.pr ? `PR #${origin.pr}` : 'commit'
    return origin?.commit ? `Closed by ${what} at ${origin.commit.slice(0, 8)}` : `Closed by ${what}`
  })

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

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={number > 0 ? `D-${number}` : 'Issue'}
  chips={issueState ? [{ label: stateWords[issueState], tone: stateTone[issueState] }] : []}
  summary={[title, who ? `filed by ${who}` : ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}◆{/snippet}
  {#snippet detail()}
  <div class="body">
    {#if files.length}
      <ul class="files">
        {#each files as file, i (i)}
          {@const parts = file.path.split('/')}
          <li>{#each parts as part, j (j)}{part}{#if j < parts.length - 1}/<wbr />{/if}{/each}{#if file.line}<span class="line">:{file.line}</span>{/if}</li>
        {/each}
      </ul>
    {/if}
    {#if payload?.symbols?.length}<p class="meta">Symbols: {payload.symbols.join(', ')}</p>{/if}
    {#if payload?.cost || payload?.direction}
      <p class="meta">{payload.cost}{#if payload.cost && payload.direction}{' · '}{/if}{payload.direction}</p>
    {/if}
    {#if payload?.assignee}<p class="meta">Assigned to {short(payload.assignee, room?.name)}</p>{/if}
    {#if closedLine}
      <p class="meta">{closedLine}</p>
    {:else if payload?.reason}
      <p class="reason">{payload.reason}</p>
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
  {/snippet}
</EventRow>

<style>
  .files { margin: 0 0 6px; padding: 0; list-style: none; font: var(--t-sm)/1.6 var(--mono); color: var(--muted); overflow-wrap: anywhere; }
  /* A path breaks at its slashes, so the line number stays with the file name
     and a phone never splits ":41" into ":4" and "1". */
  .files .line { white-space: nowrap; }
  .meta { margin: 0 0 4px; color: var(--muted); font-size: var(--t-sm); }
  .reason { margin: 0 0 6px; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .acts { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-top: 4px; }
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
</style>
