<script lang="ts">
  import type { Goal } from '../api/goal.ts'
  import type { Post } from '../api/room.ts'
  import GoalGlyph from './GoalGlyph.svelte'
  import ExpediteBadge from './ExpediteBadge.svelte'
  import { expedite, goalExpedited } from './expedite.ts'
  import { stamp } from '../ui/format.ts'
  interface Props {
    post: Post
    goals?: Goal[]
    ongoal?: (id: string) => void
  }
  let { post, goals = [], ongoal = () => {} }: Props = $props()
  let id = $derived(post.text?.match(/\bgoal (g\d+)\b/i)?.[1] ?? '')
  let goal = $derived(goals.find((g) => g.id === id))
  let milestone = $derived(post.text?.match(/^Goal g\d+ (.*?) is (blocked|done):\s*(.*)$/is))
  // TODO: legacy/shim remove later — renders old goal milestone posts without titles; remove when retained room history uses the current format
  let legacy = $derived(post.text?.match(/^Goal g\d+ (blocked|done):\s*(.*)$/is))
  let verdict = $derived(milestone?.[2] ?? legacy?.[1] ?? '')
  let title = $derived(milestone?.[1] ?? goal?.text ?? '')
  let summary = $derived(milestone?.[3] ?? legacy?.[2] ?? '')
  let prefix = $derived(post.text?.split(/goal g\d+:?/i)[0] ?? '')
  let actor = $derived(prefix.match(/^(.+?) (?:added|edited|finished|dropped)/)?.[1] ?? '')
  const clip = (s: string): string => [...s].length > 60 ? [...s].slice(0, 59).join('') + '…' : s
</script>
<div class="goalpost" data-id={post.id}>
  <GoalGlyph goal={{ state: verdict === 'done' ? 'done' : /\bdropped goal\b/.test(post.text) ? 'dropped' : 'active' }} /><div class="body">
    {#if verdict}
      <button class="goal title" onclick={() => ongoal(id)}>{id} {clip(title).replace(/\.$/, '')}</button> is {verdict}: {summary}
    {:else}
      <span>{#if actor}<strong class="hue-{post.fromHue}">{actor}</strong>{prefix.slice(actor.length)}{:else}{prefix}{/if}<button class="goal" onclick={() => ongoal(id)}>{id}</button>{post.text?.split(/goal g\d+:?/i).slice(1).join('')}</span>
    {/if}
    {#if goalExpedited($expedite, { id })}<ExpediteBadge />{/if}
  </div><time title={new Date(post.ts).toLocaleString()}>{stamp(post.ts)}</time>
</div>
<style>
  .goalpost { display: flex; align-items: start; gap: 10px; margin: var(--s1) 20px; padding: 7px 0; color: var(--muted); font-size: var(--t-sm); }
  .goalpost > :global(svg) { margin-top: 3px; }
  .body { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .goal { color: var(--iris); font-size: var(--t-sm); margin: 0 var(--s1); } .goal:hover { text-decoration: underline; }
  .title { margin: 0; color: var(--ink-2); text-align: left; }
  time { font-size: var(--t-cap); color: var(--faint); white-space: nowrap; }
</style>
