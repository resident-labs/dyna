<script>
  // A titled box of RoomSettingsRow rows, the same look as a Settings group.
  // note is one plain muted line under the box.
  let { title = '', note = '', children } = $props()
</script>

{#if title}<h3>{title}</h3>{/if}
<div class="group">{@render children()}</div>
{#if note}<p class="note">{note}</p>{/if}

<style>
  h3 { font-size: var(--t-sm); font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; margin: 28px 0 var(--s2); }
  .group { background: var(--side); border-radius: 10px; border: 1px solid var(--line-soft); }
  .group > :global(* + *) { border-top: 1px solid var(--line-soft); }
  .note { margin: var(--s2) 0 0; font-size: var(--t-ui); color: var(--faint); }
</style>
