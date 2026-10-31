<script lang="ts">
  import type { HostSummary } from "../contract.gen.ts";
  import { hostLabel, hostMeters, lastSeen } from "../hosts.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One machine on Home: the name and the workspace count, then CPU, memory and disk tiles. A healthy host shows no
  // status. A host low on disk, as the engine judges it, shows the warning pill. An unreachable one shows the warning pill and its last figures dimmed with their age, or no figures if
  // it never answered. The control plane has no workspaces.
  let { host, now }: { host: HostSummary; now: number } = $props();
  let seen = $derived(host.reachable ? undefined : lastSeen(host, now));
</script>

<section class="card" aria-label={hostLabel(host.name)}>
  <h3>{hostLabel(host.name)}
    <span class="aside state">{#if host.diskLow}<StatusPill tone="warning" label="Disk low" />{/if}
      {#if !host.reachable}<StatusPill tone="error" label="Unreachable" />
      {:else if host.name !== "control-plane"}{host.boxes} {host.boxes === 1 ? "workspace" : "workspaces"}{/if}</span></h3>
  {#if host.reachable || seen}<div class="tiles" class:dimmed={!host.reachable}>{#each hostMeters(host) as meter (meter.label)}<MeterTile {meter} />{/each}</div>{/if}
  {#if seen}<p class="aside seen">{seen}</p>{/if}
</section>

<style>
  .state { display: flex; align-items: center; gap: 8px; }
  .dimmed { opacity: 0.4; }
  .seen { margin: 10px 0 0; }
</style>
