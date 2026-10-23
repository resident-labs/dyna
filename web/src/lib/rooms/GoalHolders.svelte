<script lang="ts">
  import type { Member } from '../api/room.ts'
  import Avatar from '../ui/Avatar.svelte'
  interface Props {
    members?: Member[]
    holders?: string[]
    shared?: boolean
  }
  let { members = [], holders = $bindable([]), shared = $bindable(true) }: Props = $props()
  let query = $state('')
  let open = $state(false)
  let choices = $derived(members.filter((m) => !m.owner && !m.closed && !holders.includes(m.id ?? '') && m.name.toLowerCase().includes(query.replace(/^@/, '').toLowerCase())))
  function add(m: Member) { shared = false; holders = [...holders, m.id ?? '']; query = ''; open = false }
  function remove(id: string) { holders = holders.filter((h) => h !== id); if (!holders.length) shared = true }
</script>
<div class="picker">
  <div class="chips">
    {#if shared}<span class="chip">Room</span>{/if}
    {#each holders as id}
      {@const m = members.find((m) => m.id === id)}
      <span class="chip"><Avatar name={m?.name ?? id} hue={m?.hue ?? 0} emoji={m?.emoji} size={18} /><span>{m?.name ?? id}</span><button type="button" class="remove" aria-label="Remove {m?.name ?? id}" onclick={() => remove(id)}>✕</button></span>
    {/each}
    <input aria-label="Add goal holder" placeholder={holders.length ? 'Add' : '@ Add member'} bind:value={query} onfocus={() => (open = true)} onkeydown={(e) => { if (e.key === 'Escape') { open = false; e.stopPropagation() } if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); if (choices[0]) add(choices[0]) } }} />
  </div>
  {#if open}
    <div class="choices">
      <button type="button" onclick={() => { holders = []; shared = true; query = ''; open = false }}>Room</button>
      {#each choices as m}<button type="button" onclick={() => add(m)}><Avatar name={m.name} hue={m.hue} emoji={m.emoji} size={18} /><span class="hue-{m.hue}">{m.name}</span></button>{/each}
      <button type="button" class="close" onclick={() => (open = false)}>Close</button>
    </div>
  {/if}
</div>
<style>
  .picker { position: relative; min-width: 130px; }
  .chips { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; }
  .chip { display: inline-flex; align-items: center; gap: 5px; border-radius: 12px; background: var(--raise); color: var(--muted); padding: 2px 7px; font-size: var(--t-sm); max-width: 100%; }
  .chip > span { overflow: hidden; text-overflow: ellipsis; }
  .remove { font-size: var(--t-cap); color: var(--faint); opacity: 0; }
  .remove:focus-visible { opacity: 1; }
  .chip:hover .remove { opacity: 1; }
  input { width: 95px; background: transparent; color: var(--ink); border: 0; font: inherit; font-size: var(--t-sm); padding: var(--s1); }
  input::placeholder { color: var(--muted); }
  .choices { position: absolute; top: calc(100% + 6px); left: 0; z-index: 10; padding: 5px; min-width: 180px; max-height: 240px; overflow-y: auto; border: 1px solid var(--line); border-radius: 7px; background: var(--side); box-shadow: 0 8px 20px #0006; }
  .choices button { display: flex; gap: 7px; align-items: center; width: 100%; padding: 5px 7px; font-size: var(--t-ui); text-align: left; border-radius: 4px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .choices button:hover { background: var(--hover); }
  }
  .choices button:active { background: var(--select); }
  .choices .close { color: var(--faint); font-size: var(--t-sm); }
  @media (hover: none) { .remove { opacity: 1; } }
</style>
