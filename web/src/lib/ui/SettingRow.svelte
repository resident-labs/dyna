<script lang="ts">
  // One setting: a title, a short description and an optional status on the
  // left, the control in a right column. Under 32 rem of its own width the
  // row stacks. children render under the row, such as a list it opens.
  // disabled dims the row and makes the control and children inert.
  // ontoggle makes the title a disclosure button that covers the text column,
  // so a click anywhere on the text opens the row's detail in place; expanded
  // is its state. The control stays outside the button. inline keeps a small
  // control, such as a switch, on the right at every width.
  // Use Row for lists; use this for a value the owner sets.
  import type { Snippet } from 'svelte'
  interface Props {
    title: string
    description?: string
    status?: Snippet
    control?: Snippet
    children?: Snippet
    disabled?: boolean
    id?: string
    expanded?: boolean
    ontoggle?: () => void
    inline?: boolean
  }
  let { title, description = '', status, control, children, disabled = false, id, expanded = false, ontoggle, inline = false }: Props = $props()
</script>

<div class="setting" class:disabled class:inline class:toggles={!!ontoggle} aria-disabled={disabled || undefined} {id}>
  <div class="grid">
    <div class="text">
      <h3>{#if ontoggle}<button class="toggle" aria-expanded={expanded} onclick={ontoggle}>{title}</button>{:else}{title}{/if}</h3>
      {#if description}<p>{description}</p>{/if}
      {#if status}<div class="status">{@render status()}</div>{/if}
    </div>
    {#if control}<div class="control" inert={disabled}>{@render control()}</div>{/if}
  </div>
  {#if children}<div class="more" inert={disabled}>{@render children()}</div>{/if}
</div>

<style>
  .setting { container-type: inline-size; padding: var(--s3) var(--s4); min-width: 0; }
  .disabled { opacity: 0.64; }
  .grid { display: flex; flex-direction: column; gap: var(--s3); }
  .text { display: flex; flex-direction: column; gap: var(--s1); min-width: 0; }
  h3 { margin: 0; font-size: var(--t-body); font-weight: var(--w-medium); line-height: var(--leading-tight); color: var(--ink); }
  p { margin: 0; max-width: 42rem; font-size: var(--t-sm); color: var(--muted); text-wrap: balance; }
  .status { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); font-size: var(--t-sm); color: var(--muted); }
  .control { display: flex; align-items: center; gap: var(--s2); min-width: 0; }
  .more { margin-top: var(--s3); }
  .inline > .grid { flex-direction: row; align-items: center; }
  .inline > .grid > .text { flex: 1; }
  .inline > .grid > .control { flex: none; }
  /* The disclosure button stretches over the whole text column. */
  .toggles .text { position: relative; isolation: isolate; }
  .toggles .text > :not(h3) { pointer-events: none; }
  .toggle { font: inherit; color: inherit; text-align: left; }
  .toggle::after { content: ''; position: absolute; z-index: -1; inset: calc(-1 * var(--s2)); border-radius: var(--r-xs); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .toggle:hover::after { background: var(--hover); }
  }
  .toggle:active::after { background: var(--select); }
  /* The ring moves from the title to the whole text column. */
  .toggle:focus-visible { outline-color: transparent; }
  .toggle:focus-visible::after { box-shadow: 0 0 0 2px var(--iris-edge); }
  @container (min-width: 32rem) {
    .grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(10rem, auto); align-items: center; gap: var(--s6); }
    .control { justify-content: flex-end; }
  }
</style>
