<script>
  // One Room settings section in the settings modal's pane: a title, a short
  // lede and its groups, laid out like a Settings pane. Groups use
  // RoomSettingsGroup.
  let { title, lede = '', children } = $props()
</script>

<section aria-label={title}>
  <h1>{title}</h1>
  {#if lede}<p class="lede">{lede}</p>{/if}
  {@render children()}
</section>

<style>
  h1 { font-size: var(--t-page); margin: 0 0 6px; }
  .lede { color: var(--muted); font-size: var(--t-body); margin: 0 0 var(--s2); }
</style>
