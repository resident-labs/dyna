<script lang="ts">
  // The row for a goal milestone: a glyph, the goal's link and one line. It
  // reads the typed payload of the post, so the goal and the verdict never come
  // from the text. A post stored before the typed events still has only text;
  // TODO: legacy/shim remove later — remove the text readers when that history
  // ages out.
  import GoalGlyph from './GoalGlyph.svelte'
  import ExpediteBadge from './ExpediteBadge.svelte'
  import EventDetails from './EventDetails.svelte'
  import { expedite, goalExpedited } from './expedite.ts'
  import { stamp } from '../ui/format.ts'
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface, GoalRef } from './eventRenderers.ts'
  import { goalEvent } from './eventPayloads.ts'

  let {
    post,
    notice = post.notice,
    surface = 'feed',
    goals = [],
    ongoal = undefined,
  }: {
    post: EventPost
    notice?: EventNotice
    surface?: EventSurface
    goals?: readonly GoalRef[]
    ongoal?: (id: string) => void
  } = $props()

  let payload = $derived(goalEvent(post))
  let legacyID = $derived(post.text?.match(/\bgoal (g\d+)\b/i)?.[1] ?? '')
  let milestone = $derived(post.text?.match(/^Goal g\d+ (.*?) is (blocked|done):\s*(.*)$/is))
  let legacy = $derived(post.text?.match(/^Goal g\d+ (blocked|done):\s*(.*)$/is))
  let legacyVerdict = $derived(milestone?.[2] ?? legacy?.[1] ?? '')
  let legacyPrefix = $derived(post.text?.split(/goal g\d+:?/i)[0] ?? '')
  let legacyActor = $derived(legacyPrefix.match(/^(.+?) (?:added|edited|finished|dropped)/)?.[1] ?? '')

  let id = $derived(payload?.goalId ?? legacyID)
  let title = $derived(payload?.title ?? milestone?.[1] ?? goals.find((g) => g.id === id)?.text ?? '')
  let verdict = $derived(payload ? (payload.type === 'finished' ? 'done' : '') : legacyVerdict)
  let summary = $derived(payload?.summary ?? milestone?.[3] ?? legacy?.[2] ?? '')
  let actor = $derived(payload?.actor ?? legacyActor)
  // The words before the goal link, and the goal's own text after it.
  let rest = $derived(payload ? ` ${payload.type} goal ` : legacyActor ? legacyPrefix.slice(legacyActor.length) : legacyPrefix)
  let tail = $derived(payload ? `: ${payload.title}` : (post.text?.split(/goal g\d+:?/i).slice(1).join('') ?? ''))
  let state = $derived(payload ? payload.state : verdict === 'done' ? 'done' : /\bdropped goal\b/.test(post.text ?? '') ? 'dropped' : 'active')
  const clip = (s: string): string => ([...s].length > 60 ? [...s].slice(0, 59).join('') + '…' : s)
</script>

<div class="goalpost" data-id={post.id}>
  <GoalGlyph goal={{ state }} /><div class="body">
    {#if verdict}
      <button class="goal title" onclick={() => ongoal?.(id)}>{id} {clip(title).replace(/\.$/, '')}</button> is {verdict}: {summary}
    {:else}
      <span>{#if actor}<strong class="hue-{post.fromHue}">{actor}</strong>{rest}{:else}{rest}{/if}<button class="goal" onclick={() => ongoal?.(id)}>{id}</button>{tail}</span>
    {/if}
    {#if goalExpedited($expedite, { id })}<ExpediteBadge />{/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div><time title={new Date(post.ts ?? 0).toLocaleString()}>{stamp(post.ts)}</time>
</div>
<style>
  .goalpost { display: flex; align-items: start; gap: 10px; margin: var(--s1) 20px; padding: 7px 0; color: var(--muted); font-size: var(--t-sm); }
  .goalpost > :global(svg) { margin-top: 3px; }
  .body { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .goal { color: var(--iris); font-size: var(--t-sm); margin: 0 var(--s1); } .goal:hover { text-decoration: underline; }
  .title { margin: 0; color: var(--ink-2); text-align: left; }
  time { font-size: var(--t-cap); color: var(--faint); white-space: nowrap; }
</style>
