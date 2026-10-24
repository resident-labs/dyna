<script lang="ts">
  // The row for a goal milestone: a glyph, the goal's link and one line. It
  // reads the typed payload of the post, so the goal and the verdict never come
  // from the text. A post stored before the typed events still has only text;
  // TODO: legacy/shim remove later — remove the text readers when that history
  // ages out.
  import GoalGlyph from './GoalGlyph.svelte'
  import EventDetails from '../rooms/EventDetails.svelte'
  import EventRow from '../rooms/EventRow.svelte'
  import { expedite, goalExpedited } from '../rooms/expedite.ts'
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface, GoalRef } from '../rooms/eventRenderers.ts'
  import { goalEvent } from '../rooms/eventPayloads.ts'

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
  let rowTitle = $derived(verdict ? id : `${actor}${rest}${id}`.replace(/\s+/g, ' ').trim())
  let rowSummary = $derived(verdict ? [clip(title).replace(/\.$/, ''), summary].filter(Boolean).join(' · ') : payload?.type === 'amended' ? summary : tail.replace(/^:\s*/, ''))
  let chips = $derived([
    { label: state, tone: state === 'done' ? 'sage' : state === 'dropped' ? 'coral' : 'iris' },
    ...(goalExpedited($expedite, { id }) ? [{ label: 'expedited', tone: 'iris' }] : []),
  ])
</script>

{#snippet icon()}<GoalGlyph goal={{ state }} />{/snippet}
{#snippet detail()}
  <button class="link" onclick={() => ongoal?.(id)}>Open goal</button>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={rowTitle}
  summary={rowSummary}
  {chips}
  {icon}
  detail={surface === 'dm' ? detail : undefined}
  onclick={surface === 'feed' ? () => ongoal?.(id) : null}
/>
