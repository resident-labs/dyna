<script lang="ts">
  // A state that needs the owner, drawn as T3 Code draws thread status: a small dot and a short label in the tone's
  // color, with no fill or border. In a dense row, dot shows a larger dot alone; its label moves to the tooltip.
  // word shows the label alone, for a status word inside a muted line whose dot shows elsewhere.
  // A count is the one filled form: a number on an icon's corner, as v1 and Discord count what waits. Its parent
  // names the count, places it and sets --ring to the color behind it.
  // Working's dot has a still, soft halo, as in the chosen room mockup (note 6); nothing moves.
  let { tone, label, title = label, dot = false, word = false, count }: {
    tone: "warning" | "error" | "blocked" | "failed" | "working"; label: string; title?: string; dot?: boolean; word?: boolean; count?: number;
  } = $props();
  let error = $derived(tone === "error" || tone === "blocked" || tone === "failed");
</script>
{#if count !== undefined}<span class="count" class:error aria-hidden="true">{count > 99 ? "99+" : count}</span>
{:else}<span class="status" class:dot class:error class:working={tone === "working"} {title} role={dot ? "img" : undefined}
  aria-label={dot ? title : undefined}>{#if !word}<i></i>{/if}{#if !dot}{label}{/if}</span>{/if}
<style>
  .status { display: inline-flex; align-items: center; gap: 5px; flex: none; color: var(--attention); opacity: 0.9;
    font-size: 12.5px; font-weight: 500; line-height: 1.25; white-space: nowrap; }
  .error { color: var(--danger); }
  .working { color: var(--success); }
  .count { position: absolute; min-width: 18px; height: 18px; padding: 0 5px; box-sizing: border-box; display: grid; place-items: center; border-radius: 9px;
    background: var(--attention); color: var(--bg); box-shadow: 0 0 0 var(--ring-size, 2px) var(--ring, var(--bg)); font-size: 11px; font-weight: 600;
    font-variant-numeric: tabular-nums; line-height: 1; }
  .count.error { background: var(--danger); color: var(--bg); }
  i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
  .dot { width: 14px; height: 14px; justify-content: center; }
  .dot i { width: 9px; height: 9px; }
  .working i { box-shadow: 0 0 0 3px color-mix(in oklab, currentColor 22%, transparent); }
</style>
