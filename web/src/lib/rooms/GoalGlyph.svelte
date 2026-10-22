<script>
  import { goalTone } from './goals.js'
  let { goal = { state: 'active' }, size = 14, neutral = false } = $props()
</script>
<svg width={size} height={size} viewBox="0 0 14 14" class="glyph {neutral ? 'neutral' : goalTone(goal)}" aria-hidden="true">
  <circle cx="7" cy="7" r="5.8" stroke="currentColor" stroke-width="2" fill={goal.state === 'done' ? 'currentColor' : 'none'} />
  {#if goal.state === 'dropped'}<path d="m3 11 8-8" stroke="currentColor" stroke-width="1.6" />{/if}
</svg>
<style>
  .glyph { flex: none; }
  .gold { color: var(--gold); } .sage { color: var(--sage); } .faint { color: var(--faint); }
</style>
