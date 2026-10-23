<script>
  import { tick } from 'svelte'
  // The add box of a room work tab: a textarea that grows while focused,
  // Enter submits, Shift+Enter breaks the line. `tools` sits left of the
  // submit button.
  let { value = $bindable(''), label, placeholder, submit, maxlength = 2000, busy = false, error = '', onsubmit, tools } = $props()
  let field = $state()
  let focused = $state(false)
  function fit(el, lines = focused ? 3 : 1) {
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(150, Math.max(lines * 21 + 24, el.scrollHeight)) + 'px'
  }
  async function add() {
    if (!value.trim() || busy) return
    await onsubmit()
    await tick(); fit(field)
  }
</script>
<form class="add" onsubmit={(e) => { e.preventDefault(); add() }}>
  <textarea aria-label={label} {placeholder} {maxlength} rows="1" bind:this={field} bind:value oninput={(e) => fit(e.currentTarget)} onfocus={(e) => { focused = true; fit(e.currentTarget, 3) }} onblur={(e) => { focused = false; fit(e.currentTarget, 1) }} onkeydown={(e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); add() } }}></textarea>
  <div class="addtools">{@render tools?.()}<button class="submit" disabled={busy || !value.trim()}>{submit}</button></div>
</form>
{#if error}<p class="error" role="alert">{error}</p>{/if}
<style>
  .add { margin: 0 var(--s5) 18px; border: 1px solid var(--line); border-radius: 8px; background: var(--side); }
  textarea { display: block; width: 100%; background: transparent; border: 0; resize: none; color: var(--ink); font: inherit; font-size: var(--t-body); line-height: var(--leading); padding: var(--s3) 14px; min-height: 45px; }
  textarea::placeholder { color: var(--faint); }
  .addtools { display: flex; align-items: center; justify-content: flex-end; gap: var(--s3); flex-wrap: wrap; border-top: 1px solid var(--line-soft); padding: var(--s2) 10px; }
  .submit { padding: 5px var(--s3); border-radius: 5px; background: var(--raise); color: var(--ink); font-size: var(--t-sm); font-weight: 600; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .submit:hover { background: var(--raise-hover); }
  }
  .submit:active { background: var(--select); } .submit:disabled { opacity: .4; cursor: default; }
  .error { color: var(--coral); font-size: var(--t-ui); margin: 0 var(--s5) 10px; }
  @media (max-width: 760px) { .add { margin: 0 var(--s4) 14px; } }
</style>
