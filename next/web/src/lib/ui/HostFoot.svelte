<script lang="ts">
  import { hostLabel, hostLoads, watchHosts } from "../hosts.ts";
  // The hosts' load at the foot of the sessions panel, so a slow machine shows from the room (mockup note 7, fix 7):
  // up to 3 hosts, busiest first, each its name and "CPU 19% · Mem 32%" muted. A figure at 90% or more takes the
  // warning color, as on Home's meters. It links to Machines on Home. Before the first answer, or with no host that
  // reports load, it shows nothing; a failed call keeps the last answer.
  type Navigate = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  let { navigate }: { navigate: Navigate } = $props();
  let hosts: ReturnType<typeof hostLoads> = $state([]);
  $effect(() => watchHosts(list => { hosts = hostLoads(list); }, () => {}));
</script>

{#if hosts.length}<a class="hosts" href="/#machines" onclick={navigate} title="Machines on Home">
  {#each hosts as host (host.name)}<span class="host"><span class="name">{hostLabel(host.name)}</span>
    <span class="load">{#each host.meters as m, i (m.label)}{#if i}{" · "}{/if}<span class:busy={m.busy}>{m.label === "Memory" ? "Mem" : m.label} {m.percent}%</span>{/each}</span></span>{/each}</a>{/if}

<style>
  /* It stays at the bottom of the panel while the list scrolls above it. */
  .hosts { position: sticky; bottom: 0; margin-top: auto; display: grid; gap: 6px; padding: 12px 16px; border-top: 1px solid var(--line);
    background: var(--sidebar); color: var(--muted); font-size: 12.5px; text-decoration: none; }
  .hosts:hover { box-shadow: inset 0 0 0 100vmax var(--hover); }
  .host { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .name { flex: 1; min-width: 0; overflow: hidden; color: var(--text); font-size: 13px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .load { flex: none; font-variant-numeric: tabular-nums; }
  .busy { color: var(--attention); }
</style>
