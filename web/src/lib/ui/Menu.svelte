<script lang="ts">
  // A small context menu drawn in the DOM, so the web client can reuse it.
  // items: [{ label, hint?, danger?, gap? (separator above), run }].
  import { onMount, tick, untrack } from 'svelte'

  interface MenuItem {
    label: string
    run: () => unknown
    hint?: string
    danger?: boolean
    gap?: boolean
    disabled?: boolean
  }

  interface Props {
    // x and y place the menu, in viewport pixels.
    x?: number
    y?: number
    title?: string
    items?: MenuItem[]
    onclose: () => void
  }
  let { x = 0, y = 0, title = '', items = [], onclose }: Props = $props()

  let box = $state<HTMLDivElement | undefined>()
  let list = $state<HTMLDivElement | undefined>()
  let pick = $state(0)
  let pos = $state({ left: x, top: y })

  // The visual viewport excludes the software keyboard and reflects zoom.
  function place() {
    if (!box) return
    const viewport = window.visualViewport
    const left = viewport?.offsetLeft ?? 0
    const top = viewport?.offsetTop ?? 0
    const width = viewport?.width ?? window.innerWidth
    const height = viewport?.height ?? window.innerHeight
    box.style.maxWidth = Math.max(0, Math.min(280, width - 16)) + 'px'
    box.style.minWidth = Math.max(0, Math.min(208, width - 16)) + 'px'
    box.style.maxHeight = Math.max(0, height - 16) + 'px'
    const r = box.getBoundingClientRect()
    pos = {
      left: Math.max(left + 8, Math.min(x, left + width - r.width - 8)),
      top: Math.max(top + 8, Math.min(y + r.height > top + height - 8 ? y - r.height : y, top + height - r.height - 8)),
    }
  }
  function reveal(item: HTMLElement | undefined) {
    if (!item || !list) return
    const itemRect = item.getBoundingClientRect()
    const listRect = list.getBoundingClientRect()
    if (itemRect.top < listRect.top) list.scrollTop -= listRect.top - itemRect.top
    else if (itemRect.bottom > listRect.bottom) list.scrollTop += itemRect.bottom - listRect.bottom
  }
  $effect(() => {
    x; y; items; title; box
    untrack(place)
  })
  onMount(() => {
    let alive = true
    const viewport = window.visualViewport
    const resize = () => {
      place()
      reveal(box?.querySelectorAll<HTMLElement>('[role=menuitem]')[pick])
    }
    window.addEventListener('resize', resize)
    viewport?.addEventListener('resize', resize)
    viewport?.addEventListener('scroll', resize)
    tick().then(() => {
      if (!alive) return
      place()
      focusAt(0)
    })
    return () => {
      alive = false
      window.removeEventListener('resize', resize)
      viewport?.removeEventListener('resize', resize)
      viewport?.removeEventListener('scroll', resize)
    }
  })

  function focusAt(i: number, direction = 1) {
    let next = (i + items.length) % items.length
    for (let n = 0; n < items.length; n++, next = (next + direction + items.length) % items.length) {
      const item = items[next]
      if (item && !item.disabled) {
        pick = next
        const node = box?.querySelectorAll<HTMLElement>('[role=menuitem]')[pick]
        node?.focus({ preventScroll: true })
        reveal(node)
        return
      }
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') { e.preventDefault(); focusAt(pick + 1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); focusAt(pick - 1, -1) }
    else if (e.key === 'Home') { e.preventDefault(); focusAt(0) }
    else if (e.key === 'End') { e.preventDefault(); focusAt(items.length - 1, -1) }
    else if (e.key === 'Escape' || e.key === 'Tab') { e.preventDefault(); onclose() }
  }

  function choose(it: MenuItem) {
    if (it.disabled) return
    onclose()
    it.run()
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="catch" onclick={onclose} oncontextmenu={(e) => { e.preventDefault(); onclose() }}></div>
<div class="menu" role="menu" aria-label={title} tabindex="-1" bind:this={box} style:left={pos.left + 'px'} style:top={pos.top + 'px'} onkeydown={keydown}>
  {#if title}<div class="head">{title}</div>{/if}
  <div class="items" bind:this={list}>
  {#each items as it, i}
    {#if it.gap}<div class="sep" role="separator"></div>{/if}
    <button role="menuitem" disabled={it.disabled} class:danger={it.danger} tabindex={i === pick ? 0 : -1} onfocus={() => (pick = i)} onmouseenter={() => focusAt(i)} onclick={() => choose(it)}>
      <span>{it.label}</span>
      {#if it.hint}<span class="hint">{it.hint}</span>{/if}
    </button>
  {/each}
  </div>
</div>

<style>
  /* Above the phone drawer (50) and member sheet (45), so the menu opened
     from a sheet row shows on top of it; below Confirm (60), which Stop and
     Close open next. */
  button:disabled { opacity: .5; cursor: default; }
  .catch { position: fixed; inset: 0; z-index: 57; touch-action: none; }
  .menu {
    position: fixed;
    z-index: 58;
    min-width: 208px;
    max-width: 280px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: #121113;
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    padding: 6px;
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.5);
    outline: none;
  }
  .items { min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
  .head {
    flex: none;
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
