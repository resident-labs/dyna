<script lang="ts">
  let { dm = false }: { dm?: boolean } = $props()
</script>

<p class:note={dm} class:event-note={dm} class:unknown-event={!dm} role="status">Dyna updated. Reload to see this event.</p>

<style>
  .unknown-event { margin: var(--s1) 0; padding: 6px 10px; border: 1px dashed var(--line); border-radius: 6px; color: var(--muted); font-size: var(--t-sm); }
  .note { margin: var(--s1) 0; font-size: var(--t-sm); color: var(--faint); }
  .event-note { margin-left: var(--gutter); }
</style>
