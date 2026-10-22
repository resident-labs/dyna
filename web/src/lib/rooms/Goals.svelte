<script>
  import LoadOlder from '../ui/LoadOlder.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import GoalGlyph from './GoalGlyph.svelte'
  import TabHeader from './TabHeader.svelte'
  import ServiceHeader from '../services/ServiceHeader.svelte'
  import Row from '../ui/Row.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { goalTree, goalStatus, goalTone, goalFilter, itemCountLabel } from './goals.js'
  import GoalJournal from './GoalJournal.svelte'
  import { createGoalEvents } from './goalEvents.ts'
  import ExpediteBadge from './ExpediteBadge.svelte'
  import { expedite, goalExpedited } from './expedite.ts'
  import { api } from '../../api.js'
  let { older = false, loadingOlder = false, onolder = () => {}, roomId, goals = [], members = [], selected = '', onselect, ongoal = onselect, onexpedite = null } = $props()
  let filter = $state('active')
  let now = $state(Date.now())
  $effect(() => { const timer = setInterval(() => (now = Date.now()), 60000); return () => clearInterval(timer) })
  let rows = $derived(goalTree(goals, filter))
  const member = (id) => members.find((m) => m.id === id || m.address === id)
  const name = (id) => member(id)?.name ?? id
  // The Journal tab loads the room's goal events when it opens.
  let journal = $state({ items: [], older: false, loading: false, error: '' })
  const journalLoader = createGoalEvents(api, (s) => (journal = s))
  $effect(() => { if (filter === 'journal' && roomId) journalLoader.open(roomId, '') })
  // One call carries every goal's item counts for the list.
  let counts = $state({})
  $effect(() => { if (!roomId) return; api.goalItemCounts(roomId).then((c) => (counts = c ?? {}), () => {}) })

</script>
<section class="goals" aria-label="Goals">
  <TabHeader title="Goals" blurb="Plain-English goals with a mechanical status from facts. Holders write the Journal; items delegate the work." filters={['active', 'done', 'journal'].map((f) => ({ id: f, label: f[0].toUpperCase() + f.slice(1) }))} bind:filter />
  <ServiceHeader {roomId} role="keeper" />
  <div class="list">
    {#if filter === 'journal'}
      <GoalJournal {...journal} {members} onolder={() => journalLoader.older()} ongoal={async (id) => { const g = await ongoal(id); if (g) filter = goalFilter(g) }} />
    {:else}
    {#if rows.length === 0}<EmptyState title={filter === 'done' ? 'No done goals' : 'No active goals'}>Root and agents add goals with <code>dyna goal</code>.</EmptyState>{/if}
    {#each rows as { goal: g, depth, context } (g.id)}
      {@const count = counts[g.id]}
      {@const status = goalStatus(g, now)}
      {@const held = g.shared ? [] : (g.holders ?? []).map(member).filter(Boolean)}
      <div class="branch" class:child={depth > 0} style="margin-left: {depth * 24}px">
        <Row text={g.text} selected={selected === g.id} {context} onclick={() => onselect(g.id)}>
          {#snippet lead()}<GoalGlyph goal={g} />{/snippet}
          {#snippet meta()}{#if goalExpedited($expedite, g)}<ExpediteBadge />{/if}<span class="avatars">{#if g.shared}<span>Room</span>{:else}{#each held.slice(0, 4) as m}<Avatar name={m.name} hue={m.hue} emoji={m.emoji} size={18} />{/each}{#if held.length}<span>{held[0].name}{held.length > 1 ? ` +${held.length - 1}` : ''}</span>{/if}{/if}</span><span>· from {name(g.setter)}</span><span class="status {g.state === 'active' ? '' : goalTone(g)}">{status}</span>{/snippet}
          {#snippet trail()}{#if count?.[1]}<span class="count">{itemCountLabel(count)}</span>{/if}{/snippet}
        </Row>
        {#if g.state === 'active' && onexpedite}<button class="xp" onclick={() => onexpedite(g)}>{goalExpedited($expedite, g) ? 'Stop expediting' : 'Expedite'}</button>{/if}
      </div>
    {/each}
    <LoadOlder {older} loading={loadingOlder} onclick={onolder} />
    {/if}
  </div>
</section>
<style>
  .goals { display: flex; flex-direction: column; min-height: 0; flex: 1; }
  .list { overflow-y: auto; padding: 0 var(--s4) var(--s5); flex: 1; }
  .branch { position: relative; }
  .branch.child { border-left: 1px solid var(--line); }
  .xp { position: absolute; top: var(--s2); right: var(--s2); font-size: var(--t-cap); color: var(--muted); background: var(--raise); border-radius: 5px; padding: 3px 8px; opacity: 0; transition: color var(--motion-fast) var(--ease), opacity var(--motion-fast) var(--ease); }
  .branch:hover .xp, .xp:focus-visible { opacity: 1; }
  @media (hover: hover) { .xp:hover { color: var(--ink); } }
  @media (hover: none) { .xp { opacity: 1; } }
  .avatars { display: flex; align-items: center; gap: 3px; }
  .avatars :global(.avatar) + :global(.avatar) { margin-left: -6px; }
  .status { overflow-wrap: anywhere; } .coral { color: var(--coral); } .faint { color: var(--faint); } .sage { color: var(--sage); }
  .count { color: var(--faint); font-size: var(--t-sm); white-space: nowrap; padding-top: 3px; }
  @media (max-width: 760px) { .list { padding: 0 var(--s2) var(--s4); } }
</style>
