<script lang="ts">
  import { dialog as modal } from '../ui/dialog.ts'
  import { api } from '../../api.ts'
  import { itemCountLabel } from '../goals/goals.ts'
  import type { Goal } from '../api/goal.ts'

  interface Props {
    goals: Goal[]
    roomId: string
    session: string
    expeditedId: string
    onpick: (g: Goal) => void
    onstop: () => void
    onclose: () => void
  }
  // A session with several goals picks one here; App expedites it and asks
  // first when it replaces the room's current goal.
  let { goals, roomId, session, expeditedId, onpick, onstop, onclose }: Props = $props()
  let close = $state<HTMLButtonElement>()
  let counts = $state<Record<string, [number, number]>>({})
  let here = $derived(goals.some((g) => g.id === expeditedId))
  $effect(() => {
    const id = roomId
    if (!id) return
    api.goalItemCounts(id).then((c) => (counts = c ?? {}), () => {})
  })
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="card" role="dialog" aria-modal="true" aria-label="Expedite a goal" tabindex="-1" use:modal={{ close: onclose, initial: () => close }} onclick={(e) => e.stopPropagation()}>
    <header>
      <div><h2>Expedite a goal</h2><p>Choose one goal held by {session}. Its agents and linked PRs get the first review and merge.</p></div>
      <button class="close" bind:this={close} aria-label="Close" onclick={onclose}>×</button>
    </header>
    {#if expeditedId}<p class="hint">Goal {expeditedId} is expedited now. A room has one expedited goal, so choosing another replaces it.</p>{/if}
    <ul class="rows">
      {#each goals as g (g.id)}
        {@const count = counts[g.id]}
        {@const current = g.id === expeditedId}
        <li>
          <button class="row" class:current disabled={current} onclick={() => onpick(g)} aria-label={current ? `${g.id} is expedited` : `Expedite ${g.id}`}>
            <span class="chip">{g.id}</span>
            <span class="text"><span class="title">{g.text}</span>{#if itemCountLabel(count)}<span class="count">{itemCountLabel(count)}</span>{/if}</span>
            <span class="mark">{current ? '✓' : ''}</span>
          </button>
        </li>
      {/each}
    </ul>
    <footer>
      {#if here}<button class="quiet" onclick={onstop}>Stop expediting</button>{/if}
      <button class="quiet" onclick={onclose}>Cancel</button>
    </footer>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; z-index: 59; padding: 20px; background: #08080aaa; display: grid; place-items: center; }
  .card { width: min(460px, 100%); max-height: calc(100dvh - 40px); overflow-y: auto; border-radius: 12px; background: var(--side); color: var(--ink); padding: 22px; box-shadow: 0 24px 64px #0008; outline: none; }
  header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--s3); }
  header > div { min-width: 0; }
  h2 { margin: 0; font-size: var(--t-head); }
  header p { margin: 5px 0 16px; color: var(--muted); font-size: var(--t-ui); line-height: var(--leading); }
  .close { font-size: var(--t-display); min-width: 32px; min-height: 32px; }
  .hint { font-size: var(--t-ui); color: var(--muted); line-height: var(--leading); overflow-wrap: anywhere; margin: 0 0 var(--s3); }
  .rows { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
  .row { width: 100%; display: flex; align-items: flex-start; gap: var(--s3); padding: 10px 12px; min-height: 56px; border: 1px solid var(--line); border-radius: 8px; background: var(--main); color: var(--ink-2); text-align: left; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  .row.current { background: var(--iris-wash); border-color: var(--iris-edge); color: var(--ink); }
  .row:disabled { cursor: default; pointer-events: none; }
  @media (hover: hover) {
    .row:hover { background: var(--hover); color: var(--ink); }
  }
  .row:active { background: var(--select); }
  .chip { flex: none; font-size: var(--t-sm); color: var(--iris); font-weight: 600; padding-top: 2px; }
  .text { min-width: 0; display: grid; gap: 2px; }
  .title { font-size: var(--t-body); line-height: var(--leading); display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; }
  .count { font-size: var(--t-sm); color: var(--faint); }
  .mark { flex: none; margin-left: auto; padding-top: 2px; color: var(--iris); font-weight: 600; }
  footer { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--s2); margin-top: var(--s4); }
  footer button { padding: var(--s2) var(--s3); border-radius: 6px; font-size: var(--t-ui); }
  .quiet { color: var(--muted); background: var(--hover); }
  @media (max-width: 480px) { .backdrop { padding: var(--s4); } .card { padding: 18px; max-height: calc(100dvh - 32px); } }
</style>
