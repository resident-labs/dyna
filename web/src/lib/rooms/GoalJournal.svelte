<script lang="ts">
  // The goal journal: every event about the room's goals, newest first —
  // holder notes and item changes. See goalEvents.ts.
  import type { Member } from '../api/room.ts'
  import type { GoalEvent } from './goalEvents.ts'
  import Avatar from '../ui/Avatar.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import LoadOlder from '../ui/LoadOlder.svelte'
  interface Props {
    items?: GoalEvent[]
    members?: Member[]
    older?: boolean
    loading?: boolean
    error?: string
    onolder?: () => void
    ongoal?: (id: string) => void
  }
  let { items = [], members = [], older = false, loading = false, error = '', onolder = () => {}, ongoal = () => {} }: Props = $props()
  const member = (id: string) => members.find((m) => m.id === id || m.address === id)
  const name = (id: string): string => member(id)?.name ?? String(id).split(':').pop() ?? String(id)
  const when = (at: string) => new Date(at).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  const VERBS: Record<string, string> = { note: 'noted', added: 'added an item', done: 'finished an item', dropped: 'dropped an item', reopened: 'reopened an item', assigned: 'assigned an item', edited: 'edited an item', returned: 'an item returned', 'keeper-status': 'keeper check-in' }
</script>

{#if error}<p class="err">Could not load the journal: {error}</p>{/if}
{#if !items.length && !loading && !error}
  <EmptyState title="No journal yet">Holder notes and item changes land here as they happen.</EmptyState>
{/if}
<ol class="journal">
  {#each items as item (item.id)}
    <li class="entry">
      <Avatar name={name(item.by)} hue={member(item.by)?.hue ?? 0} emoji={member(item.by)?.emoji} size={22} />
      <div class="body">
        <header><span class="name hue-{member(item.by)?.hue ?? 0}">{name(item.by)}</span> {VERBS[item.event] ?? item.event}{#if item.goal}<button type="button" onclick={() => ongoal(item.goal)}>{item.goal}{item.item ? `·${item.item}` : ''}</button>{/if}<time datetime={item.at}>{when(item.at)}</time></header>
        {#if item.text}<p class="text">{item.text}</p>{/if}
      </div>
    </li>
  {/each}
</ol>
<LoadOlder {older} {loading} onclick={onolder} />

<style>
  .err { color: var(--coral); }
  .journal { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: var(--s3); }
  .entry { display: flex; gap: var(--s2); border: 1px solid var(--line-soft); border-radius: 10px; background: var(--side); padding: 10px 14px; }
  .body { min-width: 0; flex: 1; }
  header { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px; font-size: var(--t-sm); color: var(--muted); }
  .name { font-weight: 600; color: var(--ink); }
  header button { font-size: var(--t-cap); color: var(--iris); }
  time { color: var(--faint); font-size: var(--t-cap); margin-left: auto; }
  .text { color: var(--ink-2); margin: 4px 0 0; font-size: var(--t-sm); overflow-wrap: anywhere; white-space: pre-wrap; }
  @media (max-width: 600px) { .entry { padding: 8px 10px; } }
</style>
