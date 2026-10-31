<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { Status } from "../status.ts";
  // A session's avatar with its one status as a dot on the corner, as Discord marks presence. mark names the dot when
  // it stands for more than this session; otherwise the words beside the face say the status and the dot is hidden from readers.
  let { name, root, dot, mark, size = 28 }: { name: string; root: boolean; dot: Status; mark?: string; size?: number } = $props();
  const tones = { waiting: "warning", failed: "failed", working: "working" } as const;
  const words = { waiting: "Waiting", failed: "Failed", working: "Working" };
</script>

<span class="face"><Avatar kind="session" {name} {root} {size} />
  {#if dot !== "idle"}<span class="mark" aria-hidden={mark ? undefined : "true"}><StatusPill tone={tones[dot]} dot label={mark ?? words[dot]} /></span>{/if}</span>

<style>
  .face { position: relative; flex: none; display: inline-grid; }
  /* The dot sits on the lower right corner, ringed in the color behind the face (--ring, the panel's by default). */
  .mark { position: absolute; right: -4px; bottom: -4px; display: grid; }
  .mark > :global(.status) { width: 14px; height: 14px; border-radius: 50%; background: var(--ring, var(--sidebar)); opacity: 1; }
  .mark > :global(.status i) { width: 8px; height: 8px; }
</style>
