<script lang="ts">
  import type { FeedDepth } from "../feed-depth.ts";
  let { value, onchange }: { value: FeedDepth; onchange: (value: FeedDepth) => void } = $props();
</script>

<label class="feed-depth" title="Root: posts from or to this room's root or you. Leads: those posts plus conversations through depth 2. All: every post. Activity stays visible.">
  <span>Show:</span>
  <select aria-label="Feed visibility" {value} onchange={event => onchange(event.currentTarget.value as FeedDepth)}>
    <option value="root">Root</option>
    <option value="leads">Leads</option>
    <option value="all">All</option>
  </select>
</label>

<style>
  .feed-depth { display: flex; align-items: center; gap: 5px; flex-shrink: 0; color: var(--muted); font-size: 12px; }
  select { width: auto; min-height: 30px; padding: 3px 5px; border: 1px solid var(--line); border-radius: 5px; background: var(--bg); color: var(--text); font: inherit; cursor: pointer; }
  @media (max-width: 640px) {
    .feed-depth > span { display: none; }
    select { min-height: 44px; }
  }
</style>
