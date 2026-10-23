<script lang="ts">
  import type { Visual } from '../api/room.ts'
  import Avatar from '../ui/Avatar.svelte'
  import TabHeader from './TabHeader.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import VisualPlaceholder from '../files/VisualPlaceholder.svelte'
  import { fileURL } from '../../api.ts'
  import { ago } from '../ui/format.ts'
  import { checkStatus, filterVisuals } from '../files/visuals.ts'
  import { visuals as store } from '../files/visuals.svelte.ts'
  interface Props {
    roomId: string
    visuals?: Visual[]
    onopen: (visual: Visual) => void
  }
  let { roomId, visuals = [], onopen }: Props = $props()
  let filter = $state('all')
  let shown = $derived(filterVisuals(visuals, filter))
  // The store learns the room when its first list arrives.
  let loading = $derived(store.room !== roomId)
  let loaded = $state<Record<string, boolean>>({})
  const title = (v: Visual): string => v.attachment.title || v.attachment.name
  const FILTERS: { id: string; label: string }[] = [
    { id: 'all', label: 'All' },
    { id: 'mine', label: 'Mine' },
    { id: 'attention', label: 'Needs attention' },
  ]
  const blurb = 'Charts and reports that agents post as HTML cards.'
</script>

<section class="visuals" aria-label="Visuals">
  <TabHeader title="Visuals" blurb={loading ? blurb : `${visuals.length} ${visuals.length === 1 ? 'visualization' : 'visualizations'}. ${blurb}`} filters={FILTERS} bind:filter />
  <div class="body">
  {#if loading}
    <div class="grid" aria-busy="true" aria-label="Loading visuals">
      {#each Array(6) as _}<div class="skeleton"><div class="shimmer"></div><div class="line"></div><div class="line short"></div></div>{/each}
    </div>
  {:else if !visuals.length}
    <EmptyState title="No visuals yet">Agents create them with <code>dyna post --html</code>.</EmptyState>
  {:else if !shown.length && filter === 'attention'}
    <EmptyState title="Nothing needs attention">No visual has a failed or warning check.</EmptyState>
  {:else if !shown.length}
    <EmptyState title="None of yours yet">Visuals you post show here.</EmptyState>
  {:else}
    <div class="grid">
      {#each shown as v, i (v.id)}
        {@const status = checkStatus(v.attachment)}
        <button class="tile" style="--i:{Math.min(i, 12)}" onclick={() => onopen(v)} aria-label="Open {title(v)}">
          <span class="thumb">
            {#if v.attachment.thumb}
              <img src={fileURL(roomId, v.attachment.thumb)} alt="" loading="lazy" decoding="async" class:in={loaded[v.id]} onload={() => (loaded[v.id] = true)} />
            {:else}<VisualPlaceholder title={title(v)} />{/if}
          </span>
          <span class="name">{title(v)}</span>
          <span class="by">
            <Avatar name={v.post.speaker} hue={v.post.fromHue} emoji={v.post.fromEmoji} owner={v.post.owner} size={20} ring="var(--main)" />
            <span class="pill mono">v{v.attachment.version || 1}</span>
            <span class="who">{v.post.speaker} · {ago(new Date(v.post.ts).toISOString())}</span>
            <span class="dot {status}" title={status === 'none' ? 'No checks' : `Checks: ${status}`}></span>
          </span>
        </button>
      {/each}
    </div>
  {/if}
  </div>
</section>

<style>
  .visuals { flex: 1; min-height: 0; overflow: auto; }
  .body { padding: 0 var(--s5) var(--s5); }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(280px, 100%), 1fr)); gap: 20px 16px; }
  .tile { display: flex; flex-direction: column; gap: 8px; min-width: 0; text-align: left; border-radius: 8px; animation: rise var(--motion-slow) var(--ease) both; animation-delay: calc(var(--i) * var(--motion-fast) / 5); }
  .tile:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: 3px; }
  @keyframes rise { from { opacity: 0; transform: translateY(8px); } }
  .thumb { display: block; aspect-ratio: 4 / 3; overflow: hidden; border-radius: 8px; border: 1px solid var(--line-soft); background: var(--raise); transition: border-color var(--motion-fast) var(--ease); }
  .tile:hover .thumb { border-color: var(--iris-edge); }
  img { display: block; width: 100%; height: 100%; object-fit: cover; opacity: 0; transition: opacity var(--motion) var(--ease); }
  img.in { opacity: 1; }
  .name { font-size: 14px; font-weight: 600; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .by { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 12px; color: var(--muted); }
  .who { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0; }
  .pill { background: var(--select); color: var(--iris); border-radius: 10px; padding: 1px 7px; font-size: 11px; flex: none; }
  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--faint); opacity: 0.6; }
  .dot.pass { background: var(--sage); opacity: 1; }
  .dot.warn { background: var(--gold); opacity: 1; }
  .dot.fail { background: var(--coral); opacity: 1; }
  .skeleton { display: flex; flex-direction: column; gap: 8px; }
  .shimmer { aspect-ratio: 4 / 3; border-radius: 8px; }
  .shimmer, .line { background: linear-gradient(90deg, var(--raise) 0%, var(--raise-hover) 50%, var(--raise) 100%); background-size: 200% 100%; animation: shimmer 1.4s linear infinite; }
  .line { height: 12px; width: 70%; border-radius: 4px; }
  .line.short { width: 45%; }
  @keyframes shimmer { to { background-position: -200% 0; } }
  code { font: 12px var(--mono); background: var(--raise); padding: 1px 6px; border-radius: 4px; }
  @media (max-width: 760px) { .body { padding: 0 var(--s4) var(--s4); } .grid { grid-template-columns: minmax(0, 1fr); } }
</style>
