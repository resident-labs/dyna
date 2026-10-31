<script lang="ts">
  import Avatar from "./Avatar.svelte";
  // The top of a conversation: its avatar, its name large, and one muted line.
  let { kind, name, line, root = false }: { kind: "room" | "session"; name: string; line: string; root?: boolean } = $props();
</script>

<div class="intro">
  <Avatar {kind} {name} {root} size={56} />
  <h2>{name}</h2>
  <p>{line}</p>
</div>

<style>
  .intro { padding: 32px 16px 12px; }
  h2 { margin: 12px 0 2px; font-size: 26px; line-height: 1.2; }
  p { margin: 0; color: var(--muted); }
</style>
