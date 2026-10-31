<script lang="ts">
  import Avatar from "./Avatar.svelte";
  // The top of a conversation, as in Discord: its avatar, its name large and one muted line. compact keeps only the line.
  let { kind, name, label, line, root = false, compact = false }: { kind: "room" | "session"; name: string; label?: string; line: string; root?: boolean; compact?: boolean } = $props();
</script>

<div class="intro">
  {#if !compact}<Avatar {kind} {name} {label} {root} size={64} />
  <h1>{kind === "room" ? `#${name}` : name}</h1>{/if}
  <p>{line}</p>
</div>

<style>
  .intro { padding: 32px 16px 8px; }
  h1 { margin: 12px 0 4px; font-size: 24px; font-weight: 600; line-height: 1.25; overflow-wrap: anywhere; }
  p { margin: 0; color: var(--muted); }
</style>
