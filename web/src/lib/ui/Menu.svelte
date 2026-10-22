<script>
  // A small context menu drawn in the DOM, so the web client can reuse it.
  // items: [{ label, hint?, danger?, gap? (separator above), run }].
  import { onMount, tick } from 'svelte'

  let { x = 0, y = 0, title = '', items = [], onclose } = $props()

  let box = $state()
  let pick = $state(0)
  let pos = $state({ left: x, top: y })

  // Keep the menu inside the window: flip it left or up near an edge.
  onMount(async () => {
    await tick()
    const r = box.getBoundingClientRect()
    pos = {
      left: Math.max(8, Math.min(x, window.innerWidth - r.width - 8)),
      top: Math.max(8, y + r.height > window.innerHeight - 8 ? y - r.height : y),
    }
    focusAt(0)
  })

  function focusAt(i, direction = 1) {
    let next = (i + items.length) % items.length
    for (let n = 0; n < items.length; n++, next = (next + direction + items.length) % items.length) {
      if (!items[next].disabled) { pick = next; box.querySelectorAll('[role=menuitem]')[pick]?.focus(); return }
    }
  }

  function keydown(e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); focusAt(pick + 1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); focusAt(pick - 1, -1) }
    else if (e.key === 'Home') { e.preventDefault(); focusAt(0) }
    else if (e.key === 'End') { e.preventDefault(); focusAt(items.length - 1, -1) }
    else if (e.key === 'Escape' || e.key === 'Tab') { e.preventDefault(); onclose() }
  }

  function choose(it) {
    if (it.disabled) return
    onclose()
    it.run()
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="catch" onclick={onclose} oncontextmenu={(e) => { e.preventDefault(); onclose() }}></div>
<div class="menu" role="menu" aria-label={title} tabindex="-1" bind:this={box} style="left:{pos.left}px; top:{pos.top}px" onkeydown={keydown}>
  {#if title}<div class="head">{title}</div>{/if}
  {#each items as it, i}
    {#if it.gap}<div class="sep" role="separator"></div>{/if}
    <button role="menuitem" disabled={it.disabled} class:danger={it.danger} tabindex={i === pick ? 0 : -1} onfocus={() => (pick = i)} onmouseenter={() => focusAt(i)} onclick={() => choose(it)}>
      <span>{it.label}</span>
      {#if it.hint}<span class="hint">{it.hint}</span>{/if}
    </button>
  {/each}
</div>

<style>
  /* Above the phone drawer (50) and member sheet (45), so the menu opened
     from a sheet row shows on top of it; below Confirm (60), which Stop and
     Close open next. */
  button:disabled { opacity: .5; cursor: default; }
  .catch { position: fixed; inset: 0; z-index: 57; }
  .menu {
    position: fixed;
    z-index: 58;
    min-width: 208px;
    max-width: 280px;
    background: #121113;
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    padding: 6px;
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.5);
    outline: none;
  }
  .head {
    font-size: var(--t-sm);
    font-weight: 600;
    color: var(--muted);
    padding: var(--s1) var(--s2) 6px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  button {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s3);
    padding: 7px var(--s2);
    border-radius: 4px;
    font-size: var(--t-body);
    color: var(--ink-2);
    text-align: left;
    transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
  }
  button:focus { background: var(--iris-wash); color: var(--ink); outline: none; }
  @media (hover: hover) {
    button:hover { background: var(--raise-hover); color: var(--ink); outline: none; }
  }
  button:active { background: var(--select); }
  .danger { color: var(--coral); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  .danger:focus { background: color-mix(in srgb, var(--coral) 16%, transparent); color: var(--coral); }
  @media (hover: hover) {
    .danger:hover { background: var(--raise-hover); color: var(--coral); }
  }
  .danger:active { background: var(--select); }
  .hint { font-size: var(--t-sm); color: var(--faint); }
  .sep { height: 1px; background: var(--line-soft); margin: 5px var(--s1); }
  @media (max-width: 760px) {
    button { min-height: 44px; font-size: var(--t-lead); }
  }
</style>
