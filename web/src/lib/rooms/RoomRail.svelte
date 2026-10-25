<script lang="ts">
  import type { Room } from '../api/room.ts'
  import { flip } from 'svelte/animate'
  import { fade } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { motion, BASE, FAST } from '../ui/motion.ts'
  import { initials, roomLabel } from '../ui/format.ts'
  // selected is the open room's ID, or '' for the machines list, the rail's
  // own tile.
  interface Props {
    narrow?: boolean
    rooms?: Room[]
    selected?: string
    onselect: (id: string) => void
    onmenu?: ((room: Room, x: number, y: number, anchor?: HTMLElement) => void) | null
  }
  let { narrow = false, rooms = [], selected = '', onselect, onmenu = null }: Props = $props()
  // Pinned rooms first, in pin order, then the rest as Go sorts them. A
  // pinned room stays on the rail even after it goes quiet.
  let pinned = $derived(rooms.filter((r) => r.pinned).sort((a, b) => (a.pinOrder || 0) - (b.pinOrder || 0)))
  let others = $derived(rooms.filter((r) => !r.pinned && !r.archived))

  function menu(e: (MouseEvent | KeyboardEvent) & { currentTarget: HTMLButtonElement }, r: Room) {
    if (!onmenu) return
    e.preventDefault()
    const box = e.currentTarget.getBoundingClientRect()
    onmenu(r, ('clientX' in e && e.clientX) || box.right, ('clientY' in e && e.clientY) || box.top, e.currentTarget)
  }
  // Shift+F10 or the context menu key open the menu from the keyboard.
  function key(e: KeyboardEvent & { currentTarget: HTMLButtonElement }, r: Room) {
    if (e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey)) menu(e, r)
  }
</script>

{#snippet tile(r: Room)}
  <div class="slot" class:sel={selected === r.id} class:unread={r.unread > 0}>
    <span class="pill"></span>
    <button class="tile" onclick={() => onselect(r.id)} oncontextmenu={(e) => menu(e, r)} onkeydown={(e) => key(e, r)} aria-label={roomLabel(r)}>
      <span class="ini">{initials(roomLabel(r))}</span>
      {#if narrow}<span class="room-name">{roomLabel(r)}</span>{/if}
      {#if r.unread > 0}<span class="badge">{r.unread > 99 ? '99+' : r.unread}</span>{/if}
      {#if r.running > 0}<span class="live" title="{r.running} running"></span>{/if}
    </button>
    {#if narrow}
      <button class="room-actions" onclick={(e) => menu(e, r)} onkeydown={(e) => key(e, r)} aria-label="Actions for {roomLabel(r)}" aria-haspopup="menu" title="Room actions">
        <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="5" cy="10" r="1.6" fill="currentColor" /><circle cx="10" cy="10" r="1.6" fill="currentColor" /><circle cx="15" cy="10" r="1.6" fill="currentColor" /></svg>
      </button>
    {:else}
      <span class="tip" role="tooltip">{roomLabel(r)}{#if r.pinned}<em class="pin">pinned</em>{/if}{#if r.paused}<em>paused</em>{/if}{#if r.running}<em>{r.running} running</em>{/if}</span>
    {/if}
  </div>
{/snippet}

<nav class="rail" class:phone={narrow} aria-label="Rooms">
  <div class="slot" class:sel={selected === ''}>
    <span class="pill"></span>
    <button class="tile home" aria-label="Machines" title="Machines" onclick={() => onselect('')}>
      <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true">
        <rect x="4" y="4.5" width="16" height="6" rx="1.8" fill="none" stroke="currentColor" stroke-width="1.8" />
        <rect x="4" y="13.5" width="16" height="6" rx="1.8" fill="none" stroke="currentColor" stroke-width="1.8" />
        <path d="M7.5 7.5h.01M7.5 16.5h.01" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" />
      </svg>
      {#if narrow}<span class="room-name">Machines</span>{/if}
    </button>
  </div>
  <div class="sep"></div>
  <!-- Pin changes move rooms between these lists, so do not delay removal. -->
  <div class="list">
    {#each pinned as r (r.id)}
      <div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }}>{@render tile(r)}</div>
    {/each}
    {#if pinned.length && others.length}<div class="sep small" aria-hidden="true"></div>{/if}
    {#each others as r (r.id)}
      <div animate:flip={{ duration: motion(BASE), easing: cubicOut }} in:fade={{ duration: motion(FAST) }}>{@render tile(r)}</div>
    {/each}
  </div>
</nav>

<style>
  .rail {
    width: 72px;
    flex: none;
    background: var(--rail);
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: var(--s3) 0;
    gap: var(--s2);
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: var(--s2);
    overflow-y: auto;
    overflow-x: visible;
    width: 100%;
    align-items: center;
    scrollbar-width: none;
  }
  .list::-webkit-scrollbar { display: none; }
  .sep { width: 32px; height: 2px; border-radius: 1px; background: var(--line); flex: none; }
  .sep.small { width: 20px; height: 2px; background: var(--line-soft); }
  .slot { position: relative; width: 72px; display: flex; justify-content: center; }
  .pill {
    position: absolute;
    left: 0;
    top: 50%;
    width: 4px;
    height: 0;
    border-radius: 0 4px 4px 0;
    background: var(--ink);
    transform: translateY(-50%);
    transition: height var(--motion-fast) var(--ease);
  }
  .slot.unread .pill { height: 8px; }
  .slot:hover .pill { height: 20px; }
  .slot.sel .pill { height: 40px; }
  .tile {
    position: relative;
    width: 48px;
    height: 48px;
    border-radius: 50%;
    background: var(--main);
    color: var(--ink-2);
    display: grid;
    place-items: center;
    transition: border-radius var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
  }
  .ini { font-weight: 600; font-size: var(--t-lead); letter-spacing: 0.02em; }
  @media (hover: hover) {
    .slot:hover .tile { border-radius: 16px; background: var(--raise); color: var(--ink); }
  }
  .slot.sel .tile { border-radius: 16px; background: var(--select); color: var(--ink); }
  .slot:active .tile { background: var(--select); }
  .home { color: var(--iris); }
  .slot.sel .home { background: var(--select); color: var(--iris); }
  .badge {
    position: absolute;
    right: -5px;
    bottom: -5px;
    min-width: 16px;
    height: 16px;
    padding: 0 var(--s1);
    border-radius: 8px;
    background: var(--ink);
    color: #18171a;
    font-size: var(--t-cap);
    font-weight: 700;
    display: grid;
    place-items: center;
    border: 3px solid var(--rail);
    box-sizing: content-box;
    line-height: 1;
  }
  .live {
    position: absolute;
    right: -1px;
    top: -1px;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--gold);
    border: 3px solid var(--rail);
  }
  .tip {
    position: fixed;
    left: 78px;
    margin-top: 10px;
    background: #0f0e10;
    color: var(--ink);
    font-weight: 600;
    font-size: var(--t-body);
    padding: 6px 10px;
    border-radius: 6px;
    white-space: nowrap;
    pointer-events: none;
    display: none;
    z-index: 30;
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.4);
  }
  .tip em { font-style: normal; color: var(--gold); font-weight: 500; margin-left: var(--s2); font-size: var(--t-ui); }
  .tip em.pin { color: var(--muted); }
  .slot:has(:focus-visible) .tip { display: block; }
  .slot:hover .tip { display: block; }

  .rail.phone { width: 100%; flex: 1 1 45%; min-height: 144px; align-items: stretch; padding: var(--s2) 0; overflow: hidden; }
  .rail.phone .list { flex: 1; min-height: 0; align-items: stretch; gap: 2px; overflow-x: hidden; }
  .rail.phone .slot { width: 100%; align-items: center; justify-content: stretch; gap: var(--s1); padding: 0 var(--s1) 0 var(--s2); }
  .rail.phone .tile { flex: 1; display: flex; align-items: center; justify-content: flex-start; gap: var(--s2); width: auto; min-width: 0; height: auto; min-height: 44px; padding: 5px var(--s2); border-radius: 8px; background: transparent; text-align: left; }
  .rail.phone .ini { display: grid; place-items: center; flex: none; width: 32px; height: 32px; border-radius: 50%; background: var(--main); font-size: var(--t-body); }
  .rail.phone .home > svg { flex: none; box-sizing: border-box; width: 32px; height: 32px; padding: 4px; border-radius: 50%; background: var(--main); }
  .rail.phone .room-name { flex: 1; min-width: 0; overflow-wrap: anywhere; font-weight: 500; line-height: var(--leading-tight); }
  .rail.phone .badge { position: static; flex: none; min-width: 20px; height: 20px; border: 0; }
  .rail.phone .live { position: static; flex: none; width: 8px; height: 8px; border: 0; }
  .rail.phone .room-actions { display: grid; place-items: center; flex: none; width: 40px; height: 44px; border-radius: 8px; color: var(--muted); }
  .rail.phone .sep { width: calc(100% - 16px); margin: var(--s1) var(--s2); align-self: center; }
  .rail.phone .slot.sel .tile { border-radius: 8px; background: var(--select); color: var(--ink); }
  .rail.phone .slot.sel .home { color: var(--iris); }
  .rail.phone .slot:active .tile { background: var(--select); }
  @media (hover: hover) {
    .rail.phone .slot:hover .tile { border-radius: 8px; background: var(--hover); color: var(--ink); }
    .rail.phone .room-actions:hover { background: var(--hover); color: var(--ink); }
  }
</style>
