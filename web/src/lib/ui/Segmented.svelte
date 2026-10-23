<script>
  // One choice out of a few, as a row of buttons. options is [[value, label]].
  // small fits a toolbar.
  let { options, value, label, small = false, onchange } = $props()

  function key(e) {
    const i = options.findIndex(([v]) => v === value)
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key]
    if (!step) return
    e.preventDefault()
    const next = options[(i + step + options.length) % options.length]
    onchange(next[0])
    e.currentTarget.querySelectorAll('button')[options.indexOf(next)]?.focus()
  }
</script>

<div class="seg" class:small role="radiogroup" aria-label={label} tabindex="-1" onkeydown={key}>
  {#each options as [v, text] (v)}
    <button role="radio" aria-checked={value === v} tabindex={value === v ? 0 : -1} class:on={value === v} onclick={() => onchange(v)}>{text}</button>
  {/each}
</div>

<style>
  .seg { display: inline-flex; flex: none; background: var(--rail); border-radius: 7px; padding: 3px; gap: 2px; }
  .seg button { padding: 5px var(--s3); border-radius: 5px; font-size: var(--t-ui); color: var(--muted); font-weight: 500; white-space: nowrap; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .seg button:hover { color: var(--ink); }
  }
  .seg button.on { background: var(--select); color: var(--ink); }
  .seg.small { padding: 2px; }
  .seg.small button { padding: 4px 10px; font-size: var(--t-sm); }
  /* Each segment owns its touch area; neighboring targets never overlap. */
  @media (pointer: coarse) {
    .seg button, .seg.small button { min-block-size: 44px; min-inline-size: 44px; }
  }
  @media (max-width: 760px) { .seg button, .seg.small button { padding: 8px 12px; } }
</style>
