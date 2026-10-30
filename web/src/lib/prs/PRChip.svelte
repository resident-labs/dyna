<script lang="ts">
  // A small pill for an agent's tracked pull request: its number and where
  // it stands. more counts the agent's other PRs.
  import StatusPill from '../ui/StatusPill.svelte'
  import type { Tone } from '../ui/tone.ts'
  import { statusMeta, changeName, type Facts, type Meta } from './pr.ts'
  import { fade } from 'svelte/transition'
  import { motion, FAST } from '../ui/motion.ts'
  import { expedite, prExpedited } from '../rooms/expedite.ts'
  // badge marks a PR linked to the expedited goal with a bolt.
  let { pr, more = 0, short = false, number = true, meta: shown = null, badge = true }: { pr: Facts; more?: number; short?: boolean; number?: boolean; meta?: Meta | null; badge?: boolean } = $props()
  let meta = $derived(shown ?? statusMeta(pr))
  let attention = $derived<Tone | null>(meta.tone === 'coral' ? 'danger' : meta.tone === 'gold' ? 'warning' : null)
</script>

<span class="pr-chip" title="{pr.kind === 'local' ? 'Local change' : `PR #${pr.number}`}{pr.title ? ': ' + pr.title : ''} · {meta.label}{more ? ` · ${more} more` : ''}">
  {#if badge && prExpedited($expedite, pr)}<span title="Expedited" aria-label="Expedited">⚡</span>{/if}
  {#if number}<span class="num">{changeName(pr)}</span>{/if}
  {#if !short}{#key meta.label}<span class="label" in:fade={{ duration: motion(FAST) }}>{#if attention}<StatusPill tone={attention} label={meta.label} />{:else}{meta.label}{/if}</span>{/key}{/if}
  {#if more}<span class="more">+{more}</span>{/if}
</span>

<style>
  .pr-chip { display: inline-flex; align-items: center; gap: var(--s1); min-width: 0; color: var(--muted); font-size: var(--t-sm); }
  .num { font-family: var(--mono); font-size: var(--t-cap); flex: none; }
  .label { overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .more { color: var(--faint); font-weight: 400; }
</style>
