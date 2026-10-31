<script lang="ts">
  import Icon from "./Icon.svelte";
  // One way back from a room's page: the room name in the crumb when wide, a 40 px arrow on a phone.
  let { room, page, onback }: { room: string; page: string; onback: () => void } = $props();
</script>

<button type="button" class="back" onclick={onback} aria-label={`Back to ${room}`} title={`Back to ${room}`}><Icon name="back" /></button>
<button type="button" class="room" onclick={onback} aria-label={`Back to ${room}`} title={`Back to ${room}`}>{room}</button><span class="slash" aria-hidden="true">/</span><strong>{page}</strong>

<style>
  .back, .room { border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  .back:hover, .room:hover { background: var(--hover); color: var(--text); }
  .back { display: none; place-items: center; width: 40px; height: 40px; margin: -8px 0 -8px -12px; padding: 0; }
  .room { margin-left: -6px; padding: 2px 6px; font-size: 13px; }
  .slash { color: var(--muted); font-size: 13px; }
  @media (max-width: 640px) { .back { display: grid; } .room, .slash { display: none; } }
</style>
