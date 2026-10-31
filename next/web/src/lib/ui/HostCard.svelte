<script lang="ts">
  import type { HostSummary } from "../contract.gen.ts";
  import { hostLabel, hostMeters } from "../hosts.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One machine on Home: an online dot and the name, the workspace count, then CPU and memory tiles. Disk free and
  // an hour's sparkline would join as a third tile and a row under the tiles.
  // An unreachable host shows the warning pill and no figures: a failed beat clears the engine's last ones.
  // The control plane has no workspaces.
  let { host }: { host: HostSummary } = $props();
</script>

<section class="card" aria-label={hostLabel(host.name)}>
  <h3><span class="dot" class:off={!host.reachable}></span>{hostLabel(host.name)}
    {#if host.reachable && host.name !== "control-plane"}<span class="aside">{host.boxes} {host.boxes === 1 ? "workspace" : "workspaces"}</span>{/if}</h3>
  {#if !host.reachable}<p><StatusPill tone="warning">Unreachable</StatusPill></p><p class="aside">No current figures</p>
  {:else}<div class="tiles">{#each hostMeters(host) as meter (meter.label)}<MeterTile {meter} />{/each}</div>{/if}
</section>

<style>
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); }
  .dot.off { background: var(--muted); }
</style>
