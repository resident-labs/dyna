<script lang="ts">
  // The row for a session note: a rename, a move, an account change, a
  // standing-instruction change, a root-seat change or a spawn. It is quiet:
  // the feed and the session DM show the same one-line row, and the session
  // DM keeps the agent text behind a Details toggle. It reads the typed
  // payload of the post, never its text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { sessionEvent } from './eventPayloads.ts'
  import type { InstructionAction, SessionChange } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const actionWords: Record<InstructionAction, string> = { items: 'changed', body: 'changed', removed: 'removed' }
  const changeTone: Record<SessionChange, string> = {
    renamed: 'iris',
    promoted: 'sage',
    adopted: 'sage',
    account: 'gold',
    instructions: 'iris',
    seat: 'iris',
    spawned: 'iris',
    'transfer-request': 'gold',
  }

  let payload = $derived(sessionEvent(post))
  let kind = $derived<SessionChange>(payload?.type ?? 'renamed')

  // headline is the one line the row states. Every fact comes from the
  // payload, so the row and the agent text cannot disagree.
  let headline = $derived.by(() => {
    const p = payload
    if (!p) return 'Session note'
    switch (p.type) {
      case 'renamed':
        return `Session ${p.oldName ?? p.session} is now ${p.name ?? ''}`
      case 'promoted':
      case 'adopted':
        return `${p.session} moved from ${p.from ?? 'top level'} to ${p.to ?? 'top level'}`
      case 'account':
        // The DM already names the session, so it keeps the change only.
        return `${surface === 'dm' ? '' : `${p.session}: `}${p.from} moved to ${p.to}, ${reasonWord(p.reason)}`
      case 'instructions':
        return `Standing instructions changed for ${p.session}`
      case 'seat':
        return p.seat ?? 'Root seat changed'
      case 'spawned':
        return `Started @${p.name}${p.profile ? ` (${p.profile})` : ''}${p.dir ? ` in ${p.dir}` : ''}`
      case 'transfer-request': {
        // The DM already names the session, so it states the fact alone. The
        // target profile and requested handoff describe the transfer without
        // exposing its transport token. The token stays in event data and the
        // agent instructions behind Details.
        const target = p.profile ? ` for ${p.profile}` : ' for model transfer'
        return `${surface === 'dm' ? '' : `${p.session}: `}Handoff requested${target}`
      }
    }
    return 'Session note'
  })

  function reasonWord(reason?: string): string {
    if (reason === 'usage-limit') return 'the account reached its limit'
    if (reason === 'fallback') return 'the profile reached its limit'
    return 'the account changed'
  }

  // sections is the instruction change, one row per changed section.
  let sections = $derived(payload?.sections ?? [])
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={headline}
  chips={[{ label: kind, tone: changeTone[kind] }]}
  summary={sections.length ? sections.map((section) => section.name).join(', ') : ''}
>
  {#snippet icon()}✦{/snippet}
  {#snippet detail()}
  {#if sections.length}
    <ul class="sections">
      {#each sections as section, i (i)}
        <li>
          <span class="name">{section.name}</span>
          <span class="action">{actionWords[section.action]}</span>
          {#each section.added ?? [] as item (item)}<span class="item add" title={item}>+ {item}</span>{/each}
          {#each section.removed ?? [] as item (item)}<span class="item remove" title={item}>− {item}</span>{/each}
        </li>
      {/each}
    </ul>
  {:else if payload?.detail && kind !== 'spawned' && kind !== 'transfer-request'}
    <details class="more">
      <summary>Show the note</summary>
      <p class="detail">{payload.detail}</p>
    </details>
  {/if}
  {#if kind === 'transfer-request'}
    {#if surface === 'feed' && payload?.detail}
      <details class="more">
        <summary>Show the handoff instructions</summary>
        <p class="detail">{payload.detail}</p>
      </details>
    {/if}
  {/if}
  {#if payload?.detail && kind === 'spawned'}<p class="detail task">{payload.detail}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .sections { margin: 4px 0 0; padding: 0; list-style: none; display: grid; gap: 2px; }
  .sections li { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; font-size: var(--t-cap); }
  .name { color: var(--ink-2); font-weight: 600; }
  .action { color: var(--faint); }
  .item { max-width: 22ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .item.add { color: var(--sage); }
  .item.remove { color: var(--coral); }
  .more { margin: 4px 0 0; }
  .more summary { cursor: pointer; color: var(--faint); font-size: var(--t-cap); list-style: none; transition: color var(--motion-fast) var(--ease); }
  .more summary::-webkit-details-marker { display: none; }
  .more summary:hover { color: var(--ink-2); }
  .detail { margin: 4px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--muted); font-size: var(--t-sm); }
  .detail.task { margin-left: 0; }
</style>
