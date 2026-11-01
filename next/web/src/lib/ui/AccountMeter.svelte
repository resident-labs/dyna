<script lang="ts">
  import type { UsageWindow } from "../contract.gen.ts";
  import { paceOf, paceWords } from "../pace.ts";
  import { windowMeter } from "../usage.ts";
  let { window, now }: { window: UsageWindow; now: number } = $props();
  let meter = $derived(windowMeter(window, now));
  let pace = $derived(paceOf(window, now));
</script>
<div class="meter" class:busy={meter.busy}>
  <div class="line"><span>{meter.label}</span><span><strong>{meter.percent}%</strong> used</span></div>
  <div class="track" role="meter" aria-label={meter.label} aria-valuenow={meter.percent} aria-valuemin={0} aria-valuemax={100}>
    <div class="fill" style:width="{meter.percent}%"></div>
    {#if pace}<span class="marker" style:left="{100 - pace.expectedRemaining}%"></span>{/if}
  </div>
  <div class="line"><span>{pace ? paceWords[pace.status] : ""}</span><span>{meter.text}</span></div>
</div>
<style>
  .meter { min-width: 0; display: grid; gap: 4px; font-size: 12.5px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .line { display: flex; justify-content: space-between; gap: 8px; }
  .line span { white-space: nowrap; }
  strong { color: var(--text); }
  .track { position: relative; height: 6px; border-radius: 3px; background: var(--track); }
  .fill { height: 100%; border-radius: inherit; background: var(--muted); }
  .marker { position: absolute; top: -3px; width: 2px; height: 12px; background: var(--text); border-radius: 2px; }
  .busy strong { color: var(--attention); } .busy .fill { background: var(--attention-fill); }
</style>
