<script lang="ts">
  // A live session's running call and how long it has run, such as
  // "Bash: go test · 12s". A call over 30 seconds shows bold. now ticks the
  // timer; the parent owns the clock.
  import { elapsed, isLongCall } from '../ui/format.js'
  import type { Doing } from '../api/room.ts'
  let { doing, now }: { doing: Doing; now: number } = $props()
  let ms = $derived(doing.since ? now - doing.since : 0)
</script>
<span class="doing" class:long={isLongCall(ms)}>{doing.text}{#if ms}{' · ' + elapsed(ms)}{/if}</span>
<style>
  .doing { color: var(--gold); }
  .doing.long { font-weight: 600; }
</style>
