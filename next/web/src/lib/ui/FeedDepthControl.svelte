<script lang="ts">
  import type { FeedDepth } from "../feed-depth.ts";
  let { value, onchange }: { value: FeedDepth; onchange: (value: FeedDepth) => void } = $props();
</script>

<label class="feed-depth" title="Root: posts from or to this room's root or you. Leads: those posts plus conversations through depth 1. All: every post. Activity stays visible.">
  <span>Show:</span>
  <select aria-label="Feed visibility" {value} onchange={event => onchange(event.currentTarget.value as FeedDepth)}>
    <option value="root">Root</option>
    <option value="leads">Leads</option>
    <option value="all">All</option>
  </select>
</label>

<style>
  .feed-depth { display: flex; align-items: center; gap: 0; flex-shrink: 0; color: var(--muted); font-size: 12px; }
  /* A quiet word with a chevron, as the header's other controls are: no box until the pointer is on it. */
  select { appearance: none; field-sizing: content; width: auto; min-height: 30px; padding: 3px 22px 3px 8px; border: 0; border-radius: 6px; color: var(--text); font: inherit; cursor: pointer;
    background: no-repeat right 6px center / 10px url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%239c9fa6' stroke-width='3' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E"); }
  select:hover { background-color: var(--hover); }
  select:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  option { background: var(--raised); color: var(--text); }
  @media (max-width: 640px) {
    .feed-depth > span { display: none; }
    select { min-height: 44px; color: var(--muted); }
  }
</style>
