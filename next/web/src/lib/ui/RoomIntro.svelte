<script lang="ts">
  import Avatar from "./Avatar.svelte";
  // The top of a conversation: its avatar and one muted line. The header already names it.
  let { kind, name, line, root = false }: { kind: "room" | "session"; name: string; line: string; root?: boolean } = $props();
</script>

<div class="intro">
  <Avatar {kind} {name} {root} size={56} />
  <p>{line}</p>
</div>

<style>
  .intro { padding: 32px 16px 12px; }
  p { margin: 12px 0 0; color: var(--muted); }
</style>
