<script lang="ts">
  import { boxDisk, boxMeters, watchBoxStats } from "../box-stats.ts";
  import type { BoxReading, BoxStatsResponse } from "../contract.gen.ts";
  import { boxReason, boxWords, waits } from "../feed.ts";
  import { hostLabel, onHost } from "../hosts.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import StatusPill from "./StatusPill.svelte";
  // The box a session runs in, as About's rows: where it is and its size, then what it is using. Only a running box shows
  // figures; any other shows its state, never zeros. They are read while this is mounted and shown, and from no one else,
  // and only the owner gets an answer, so anyone else sees the rows the room's own state can fill. A read that fails, or
  // that the box does not answer, dims the last figures instead of dropping them. Disk comes minutes late on a large
  // volume, so its row joins when it has a figure. Before the first answer the two meters hold their place.
  let { store, session, shown }: { store: RoomStore; session: string; shown: boolean } = $props();
  let box = $derived(store.boxes.get(session));
  let host = $derived(store.hosts.get(session));
  let failure = $derived(store.boxFailures.get(session));
  let reason = $derived(store.pendingReasons.get(session));
  let running = $derived(box === "ready");
  let size: BoxStatsResponse["size"] | undefined = $state();
  let reading: BoxReading | null | undefined = $state();
  let answered = $state(false), stale = $state(false);
  $effect(() => {
    if (!shown || !box) return;
    return watchBoxStats(session, running, answer => {
      size = answer.size;
      answered = true;
      stale = !answer.reading;
      reading = answer.reading ?? reading;
    }, () => { answered = true; stale = true; });
  });
  let place = $derived([host && hostLabel(host), size?.name].filter(Boolean).join(" · "));
  let meters = $derived(running && size ? boxMeters(size, reading) : []);
  let disk = $derived(running ? boxDisk(reading) : undefined);
  let holding = $derived(running && !answered);
</script>

{#if box}
  <dl class="facts">
    <dt>Box</dt>
    <dd>{#if failure}<StatusPill tone="failed" word label={onHost("Failed", host)} /> · {boxReason(failure)}
      {:else if reason === "room-host"}<StatusPill tone="warning" word label={waits[reason]} />
      {:else if reason}{waits[reason]}
      {:else if running}{place}
      {:else if box === "stopped"}<span title="Wakes on your next message">{onHost("Stopped", host)}{#if size}{" · "}{size.name}{/if}</span>
      {:else}{onHost(boxWords[box], host)}{/if}</dd>
    {#if holding}
      {#each ["CPU", "Memory"] as label (label)}<dt>{label}</dt><dd class="meter"><span class="track"></span></dd>{/each}
    {/if}
    {#each meters as m (m.label)}
      <dt>{m.label}</dt>
      <dd class="meter" class:busy={m.busy} class:dimmed={stale}>
        <span class="track" role="meter" aria-label={m.label} aria-valuetext={m.text} aria-valuenow={m.percent} aria-valuemin={0} aria-valuemax={100}><span class="fill" style:width="{m.percent}%"></span></span>
        <span class="figure">{m.text}</span>
      </dd>
    {/each}
    {#if disk}<dt>Disk</dt><dd class="disk" class:dimmed={stale} title="Used in this box's /box volume, measured every few minutes">{disk}</dd>{/if}
  </dl>
{/if}

<style>
  /* One width for the figures, so the bars line up and the disk's figure sits under theirs. */
  .meter { display: grid; grid-template-columns: minmax(0, 1fr) minmax(96px, max-content); align-items: center; gap: 8px; }
  .track { display: block; height: 3px; border-radius: 2px; background: var(--track); overflow: hidden; }
  .fill { display: block; height: 100%; background: color-mix(in srgb, var(--muted) 70%, transparent); }
  .figure, .disk { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; text-align: right; }
  .busy .figure { color: var(--attention); }
  .busy .fill { background: var(--attention-fill); }
  .dimmed { opacity: 0.4; }
</style>
