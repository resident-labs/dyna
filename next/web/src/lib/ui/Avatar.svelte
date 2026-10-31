<script lang="ts" module>
  import { createContext } from "svelte";
  // The open room's session labels by name, from distinctInitials, so the panel, the feed and the composer agree.
  export const [sessionLabels, setSessionLabels] = createContext<() => Map<string, string>>();
</script>

<script lang="ts">
  import Icon from "./Icon.svelte";
  import { hue, initials } from "../identity.ts";
  import type { PrincipalKind } from "../contract.gen.ts";
  // The owner, the root session (the one with no parent) and the engine have fixed faces;
  // every other session, and every room, gets its name's hue. Rooms are rounded squares.
  // label replaces the name's initials, as a room's distinct initials do.
  let { kind, name, label, root = false, size = 32 }: { kind: PrincipalKind | "room"; name: string; label?: string; root?: boolean; size?: number } = $props();
  const labels = sessionLabels();
  let fixed = $derived(kind === "owner" ? "owner" : kind === "engine" || root ? "root" : undefined);
</script>

<span class="avatar {fixed}" class:room={kind === "room"} style:--hue={hue(name)} style:--size="{size}px" aria-hidden="true">
  {#if fixed}<Icon name={fixed === "owner" ? "person" : "root"} size={size / 2} />{:else}{label ?? (kind === "session" ? labels().get(name) : undefined) ?? initials(name)}{/if}
</span>

<style>
  .avatar {
    flex: none; display: inline-grid; place-items: center; width: var(--size); height: var(--size); border-radius: 50%;
    background: oklch(var(--id-face) var(--hue)); color: oklch(var(--id-initials) var(--hue));
    font-size: calc(var(--size) * 0.36); font-weight: 600; letter-spacing: 0.02em; user-select: none;
  }
  .room { border-radius: 30%; }
  .owner { background: var(--focus); color: var(--bg); }
  .root { background: var(--raised); color: var(--text); box-shadow: inset 0 0 0 1px var(--line); }
</style>
