<script lang="ts">
  // A live session's current step and how long it has run, such as
  // "Read guide command · 12s". A call over 30 seconds shows bold. now ticks
  // the timer; the parent owns the clock.
  import { elapsed, isLongCall } from '../ui/format.ts'
  import { activityLabel } from './activity.ts'
  import type { Doing } from '../api/room.ts'
  let { doing, now }: { doing: Doing; now: number } = $props()
  let ms = $derived(doing.since ? now - doing.since : 0)
</script>
<span class="doing" class:long={isLongCall(ms)}>{activityLabel(doing.text)}{#if ms}{' · ' + elapsed(ms)}{/if}</span>
<style>
  .doing { color: var(--gold); }
  .doing.long { font-weight: 600; }
</style>
