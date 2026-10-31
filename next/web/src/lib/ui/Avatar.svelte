<script lang="ts">
  import Icon from "./Icon.svelte";
  import { hue, initials } from "../identity.ts";
  import type { PrincipalKind } from "../contract.gen.ts";
  // The owner, root and the engine have fixed faces; every other session gets its name's hue.
  let { kind, name, size = 32 }: { kind: PrincipalKind; name: string; size?: number } = $props();
  let fixed = $derived(kind === "owner" ? "owner" : kind === "engine" || name === "root" ? "root" : undefined);
</script>

<span class="avatar {fixed}" style:--hue={hue(name)} style:--size="{size}px" aria-hidden="true">
  {#if fixed}<Icon name={fixed === "owner" ? "person" : "root"} size={size / 2} />{:else}{initials(name)}{/if}
</span>

<style>
  .avatar {
    flex: none; display: inline-grid; place-items: center; width: var(--size); height: var(--size); border-radius: 50%;
    background: oklch(var(--id-face) var(--hue)); color: oklch(var(--id-initials) var(--hue));
    font-size: calc(var(--size) * 0.36); font-weight: 600; letter-spacing: 0.02em; user-select: none;
  }
  .owner { background: var(--focus); color: var(--bg); }
  .root { background: var(--raised); color: var(--text); box-shadow: inset 0 0 0 1px var(--line); }
</style>
