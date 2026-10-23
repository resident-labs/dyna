<script>
  import { flip } from 'svelte/animate'
  import { fade } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { motion, BASE, FAST } from '../ui/motion.ts'
  import { initials, roomLabel } from '../ui/format.ts'
  // working: awake agents mid-turn, the count on the agents tile.
  let { rooms = [], working = 0, selected = '', onselect, onmenu = null } = $props()
  // Pinned rooms first, in pin order, then the rest as Go sorts them. A
  // pinned room stays on the rail even after it goes quiet.
  let pinned = $derived(rooms.filter((r) => r.pinned).sort((a, b) => (a.pinOrder || 0) - (b.pinOrder || 0)))
  let others = $derived(rooms.filter((r) => !r.pinned && !r.archived))

  function menu(e, r) {
    if (!onmenu) return
    e.preventDefault()
    const box = e.currentTarget.getBoundingClientRect()
    onmenu(r, e.clientX || box.right, e.clientY || box.top, e.currentTarget)
  }
  // Shift+F10 or the context menu key open the menu from the keyboard.
  function key(e, r) {
    if (e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey)) menu(e, r)
  }
</script>

{#snippet tile(r)}
  <div class="slot" class:sel={selected === r.id} class:unread={r.unread > 0}>
    <span class="pill"></span>
    <button class="tile" onclick={() => onselect(r.id)} oncontextmenu={(e) => menu(e, r)} onkeydown={(e) => key(e, r)} aria-label={roomLabel(r)}>
      <span class="ini">{initials(roomLabel(r))}</span>
      {#if r.unread > 0}<span class="badge">{r.unread > 99 ? '99+' : r.unread}</span>{/if}
      {#if r.running > 0}<span class="live" title="{r.running} running"></span>{/if}
    </button>
    <span class="tip" role="tooltip">{roomLabel(r)}{#if r.pinned}<em class="pin">pinned</em>{/if}{#if r.paused}<em>paused</em>{/if}{#if r.running}<em>{r.running} running</em>{/if}</span>
  </div>
{/snippet}

<nav class="rail" aria-label="Rooms">
  <div class="slot" class:sel={selected === ''}>
    <span class="pill"></span>
    <button class="tile home" aria-label="Active agents" title="Active agents" onclick={() => onselect('')}>
      <svg viewBox="0 0 24 24" width="26" height="26" aria-hidden="true">
        <circle cx="9" cy="8" r="3.2" fill="none" stroke="currentColor" stroke-width="1.9" />
        <path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" />
        <circle cx="16.5" cy="9" r="2.4" fill="none" stroke="currentColor" stroke-width="1.7" />
        <path d="M16 13.6c2.4 0 4 1.5 4.5 4.4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" />
      </svg>
      {#if working}<span class="badge live-n" title="{working} working">{working > 99 ? '99+' : working}</span>{/if}
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
  .badge.live-n { background: var(--gold); }
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
</style>
