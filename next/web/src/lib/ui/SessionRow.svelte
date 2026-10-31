<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import { hue } from "../identity.ts";
  // One sessions panel row: the avatar, the name in its identity color, and one muted status line.
  let { name, status, root }: { name: string; status: string; root: boolean } = $props();
</script>

<Avatar kind="session" {name} {root} size={28} />
<span class="who"><span class="name" style:--hue={hue(name)}>{name}</span><span class="status">{status}</span></span>

<style>
  .who { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .name, .status { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .name { font-weight: 600; font-size: 14px; color: oklch(var(--id-name) var(--hue)); }
  .status { color: var(--muted); font-size: 12px; }
</style>
