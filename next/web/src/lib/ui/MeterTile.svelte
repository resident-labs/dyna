<script lang="ts">
  import type { Meter } from "../hosts.ts";
  // One meter on a Home card, as v1's node page tiles: the label, the percent large with an optional muted unit, a
  // bar, and the "X of Y" or "left · resets" text muted. At 90% or more it takes the warning color.
  let { meter: m }: { meter: Meter } = $props();
</script>

<div class="tile" class:busy={m.busy}>
  <span class="name">{m.label}</span><span><strong>{m.percent}%</strong>{#if m.unit}<span class="unit">{m.unit}</span>{/if}</span>
  <span class="track" role="meter" aria-label={m.label} aria-valuetext={m.text} aria-valuenow={m.percent} aria-valuemin={0} aria-valuemax={100}>
    <span class="fill" style:width="{m.percent}%"></span></span>
  <span class="text">{m.text}</span>
</div>

<style>
  .tile { display: grid; gap: 4px; min-width: 0; align-content: start; }
  .name { color: var(--muted); font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
  strong { font-size: 24px; font-weight: 600; line-height: 1.25; font-variant-numeric: tabular-nums; }
  .unit { margin-left: 4px; color: var(--muted); font-size: 12.5px; }
  .track { height: 6px; border-radius: 3px; background: color-mix(in srgb, var(--text) 7%, transparent); overflow: hidden; }
  .fill { display: block; height: 100%; background: color-mix(in srgb, var(--muted) 70%, transparent); }
  .text { overflow: hidden; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; text-overflow: ellipsis; white-space: nowrap; }
  .busy strong, .busy .text { color: var(--attention); }
  .busy .fill { background: var(--attention); }
</style>
