<script lang="ts">
  import type { Goal } from '../api/goal.ts'
  import { goalTone } from './goals.ts'
  interface Props {
    goal?: Pick<Goal, 'state'>
    size?: number
    neutral?: boolean
  }
  let { goal = { state: 'active' }, size = 14, neutral = false }: Props = $props()
</script>
<svg width={size} height={size} viewBox="0 0 14 14" class="glyph {neutral || goal.state === 'active' ? 'neutral' : goalTone(goal)}" aria-hidden="true">
  <circle cx="7" cy="7" r="5.8" stroke="currentColor" stroke-width="2" fill={goal.state === 'done' ? 'currentColor' : 'none'} />
  {#if goal.state === 'dropped'}<path d="m3 11 8-8" stroke="currentColor" stroke-width="1.6" />{/if}
</svg>
<style>
  .glyph { flex: none; } .neutral { color: var(--muted); }
  .gold { color: var(--gold); } .sage { color: var(--sage); } .faint { color: var(--faint); }
</style>
