<script lang="ts">
  // A small pill for an agent's tracked pull request: its number and where
  // it stands. more counts the agent's other PRs.
  import Chip from '../ui/Chip.svelte'
  import { toneOf } from '../ui/tone.ts'
  import { statusMeta, changeName, type Facts, type Meta } from './pr.ts'
  import { fade } from 'svelte/transition'
  import { motion, FAST } from '../ui/motion.ts'
  import { expedite, prExpedited } from '../rooms/expedite.ts'
  // badge marks a PR linked to the expedited goal with a bolt.
  let { pr, more = 0, short = false, number = true, meta: shown = null, badge = true }: { pr: Facts; more?: number; short?: boolean; number?: boolean; meta?: Meta | null; badge?: boolean } = $props()
  let meta = $derived(shown ?? statusMeta(pr))
  let tone = $derived(meta.tone === 'done' ? 'success' : toneOf(meta.tone))
</script>

<Chip {tone} outline={meta.tone === 'done' || meta.tone === 'faint'} dot={meta.tone === 'run'} title="{pr.kind === 'local' ? 'Local change' : `PR #${pr.number}`}{pr.title ? ': ' + pr.title : ''} · {meta.label}{more ? ` · ${more} more` : ''}">
  {#if badge && prExpedited($expedite, pr)}<span title="Expedited" aria-label="Expedited">⚡</span>{/if}
  {#if number}<span class="num">{changeName(pr)}</span>{/if}
  {#if !short}{#key meta.label}<span class="label" in:fade={{ duration: motion(FAST) }}>{meta.label}</span>{/key}{/if}
  {#if more}<span class="more">+{more}</span>{/if}
</Chip>

<style>
  .num { font-family: var(--mono); font-size: var(--t-cap); flex: none; }
  .label { overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .more { color: var(--faint); font-weight: 400; }
</style>
