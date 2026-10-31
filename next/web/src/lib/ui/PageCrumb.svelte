<script lang="ts">
  import Icon from "./Icon.svelte";
  // One way back from a room's page, and its name. The header already names the room. The arrow is 40 px on touch screens.
  let { room, page, onback }: { room: string; page: string; onback: (event: MouseEvent) => void } = $props();
</script>

<button type="button" class="back" onclick={onback} aria-label={`Back to ${room}`} title={`Back to ${room}`}><Icon name="back" /></button>
<strong>{page}</strong>

<style>
  .back { display: grid; place-items: center; width: 28px; height: 28px; margin: -4px 0 -4px -6px; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  .back:hover { background: var(--hover); color: var(--text); }
  @media (max-width: 640px), (hover: none) { .back { width: 40px; height: 40px; margin: -8px 0 -8px -12px; } }
</style>
