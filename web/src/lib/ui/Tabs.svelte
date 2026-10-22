<script>
  // A row of tabs. Arrow keys, Home and End move between them. Each tab is
  // {id, label, live?, count?, aria?}; onselect runs after a pick.
  let { tabs, tab = $bindable(), label, onselect = null } = $props()
  let list = $state()

  function pick(id) {
    tab = id
    onselect?.(id)
  }

  function keydown(e) {
    const i = tabs.findIndex((t) => t.id === tab)
    const j = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : -2
    if (j === -2) return
    e.preventDefault()
    pick(tabs[(j + tabs.length) % tabs.length].id)
    setTimeout(() => list?.querySelector('[aria-selected="true"]')?.focus(), 0)
  }
</script>

<div class="tabs" role="tablist" aria-label={label} tabindex="-1" bind:this={list} onkeydown={keydown}>
  {#each tabs as t}
    <button role="tab" aria-selected={tab === t.id} tabindex={tab === t.id ? 0 : -1} class:on={tab === t.id} onclick={() => pick(t.id)} aria-label={t.aria}>{t.label}{#if t.live}<span class="lv" aria-label="running"></span>{/if}{#if t.count}<span class="count" aria-hidden="true">{t.count}</span>{/if}</button>
  {/each}
</div>

<style>
  .tabs { display: flex; gap: var(--s1); padding: 0 var(--s4); border-bottom: 1px solid var(--line-soft); flex: none; overflow-x: auto; scrollbar-width: none; }
  .tabs::-webkit-scrollbar { display: none; }
  button { position: relative; padding: 10px 10px 9px; font-size: var(--t-ui); font-weight: 600; color: var(--muted); white-space: nowrap; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    button:hover { color: var(--ink); }
  }
  button.on { color: var(--ink); }
  button.on::after { content: ''; position: absolute; left: 8px; right: 8px; bottom: -1px; height: 1.5px; border-radius: 2px; background: var(--ink); }
  button:focus-visible { outline-offset: -3px; }
  .lv { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--gold); margin-left: 6px; vertical-align: 2px; }
  .count { margin-left: 6px; font-size: var(--t-cap); font-weight: 700; background: var(--raise); color: var(--ink-2); border-radius: 8px; padding: 0 6px; }
  @media (max-width: 760px) {
    .tabs { padding: 0 6px; }
    button { min-height: 44px; padding: 0 var(--s3); }
  }
</style>
