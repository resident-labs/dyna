<script lang="ts">
  import type { HostSummary } from "../contract.gen.ts";
  import { backupProblem, hostLabel, hostMeters, largeShared, lastSeen, sharedSize } from "../hosts.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One machine on Home: the name and the workspace count, then CPU, memory and disk tiles. A healthy host shows no
  // status. A host low on disk, as the engine judges it, shows the warning pill. An unreachable one shows the warning pill and its last figures dimmed with their age, or no figures if
  // it never answered. The control plane has no workspaces. A backup problem shows its pill; a healthy backup shows nothing.
  // A room's shared folder past a quarter of the disk is a muted line under the tiles: it needs cleaning, not a decision.
  let { host, now, roomName }: { host: HostSummary; now: number; roomName: (room: string) => string } = $props();
  let seen = $derived(host.reachable ? undefined : lastSeen(host, now));
  let backup = $derived(backupProblem(host, now));
</script>

<section class="card" aria-label={hostLabel(host.name)}>
  <h3>{hostLabel(host.name)}
    <span class="aside state">{#if backup}<StatusPill tone={backup.tone} label={backup.label} title={backup.title} />{/if}
      {#if host.diskLow}<StatusPill tone="warning" label="Disk low" />{/if}
      {#if !host.reachable}<StatusPill tone="error" label="Unreachable" />
      {:else if host.name !== "control-plane"}{host.boxes} {host.boxes === 1 ? "workspace" : "workspaces"}{/if}</span></h3>
  {#if host.reachable || seen}<div class="tiles" class:dimmed={!host.reachable}>{#each hostMeters(host) as meter (meter.label)}<MeterTile {meter} />{/each}</div>{/if}
  {#if seen}<p class="aside seen">{seen}</p>{/if}
  {#each largeShared(host) as folder (folder.room)}<p class="aside shared">Shared folder of {roomName(folder.room)}: {sharedSize(folder.bytes)}</p>{/each}
</section>

<style>
  .state { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 2px 8px; }
  .dimmed { opacity: 0.4; }
  .seen, .shared { margin: 10px 0 0; }
</style>
