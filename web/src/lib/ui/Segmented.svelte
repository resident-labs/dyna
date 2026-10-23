<script lang="ts" generics="T extends string | boolean">
  // One choice out of a few, as a row of buttons. options is [[value, label]].
  // small fits a toolbar. A choice is usually a word; a setting that is on or
  // off is a boolean, so the value may be either.
  interface Props {
    options: readonly (readonly [T, string])[]
    value: T
    // label names the group; a view without a title for it keeps it unannounced.
    label?: string
    small?: boolean
    onchange: (value: T) => void
  }
  let { options, value, label, small = false, onchange }: Props = $props()

  // The arrow keys that move the choice, and by how many places. Kept in the
  // handler so the component's whole behavior sits in one function.
  function key(e: KeyboardEvent & { currentTarget: HTMLElement }) {
    const i = options.findIndex(([v]) => v === value)
    const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!step) return
    e.preventDefault()
    const next = options[(i + step + options.length) % options.length]
    if (!next) return
    onchange(next[0])
    e.currentTarget.querySelectorAll<HTMLElement>('button')[options.indexOf(next)]?.focus()
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
