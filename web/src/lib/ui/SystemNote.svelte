<script lang="ts">
  // SystemNote is a quiet, expandable line for Dyna's own notices, in the
  // channel feed and in DMs. notes: [{ id, target, text, ts }]; a run of
  // notices shares one line. Opening it lists what changed, one line per
  // change; the full instructions stay behind a second toggle. Slow-test
  // issues open to one line per test: name, time against the limit, area,
  // the PRs that hit it, and the issue (onissue opens the Issues panel).
  import PostFiles from './PostFiles.svelte'
  import { clock } from './format.ts'
  import { fullText, sameChanges } from './notices.ts'
  import { prHash } from '../prs/prlink.ts'
  import type { NoteData, SlowTest } from './notices.ts'
  import type { Attachment } from '../api/room.ts'

  interface Props {
    notes?: NoteData[]
    roomId: string
    onopenfile?: (attachment: Attachment) => void
    onissue?: ((issue: string) => void) | null
  }
  let { notes = [], roomId, onopenfile, onissue = null }: Props = $props()
  let first = $derived(notes[0])
  let slow = $derived(notes[0]?.slow ? notes.map((n) => n.slow).filter((t): t is SlowTest => !!t) : null)
  let limit = $derived(slow?.find((t) => t.limit)?.limit ?? '')
  const prs = (t: SlowTest) => t.prs.map((n) => `#${n}`).join(', ')

  function openIssue(t: SlowTest) {
    if (onissue && t.issue) onissue(t.issue)
  }
  let title = $derived.by(() => {
    const one = slow?.[0]
    if (slow && one) {
      return slow.length > 1
        ? `${slow.length} slow tests over the ${limit || 'CI'} limit`
        : [`Slow test ${one.name}`, one.time && `${one.time} of ${one.limit}`, prs(one)].filter(Boolean).join(' · ')
    }
    return first?.label ?? (notes.length > 1 ? `Dyna updated standing instructions for ${notes.length} sessions` : `Dyna updated ${first?.target}'s standing instructions`)
  })
</script>

<details class="note">
  <summary><span class="mark" aria-hidden="true">✦</span><span class="label">{title}</span><time>{clock(notes.at(-1)?.ts)}</time><span class="chev" aria-hidden="true">›</span></summary>
  <div class="panel">
    {#if slow}
      {#each slow as t (t.issue)}
        <div class="slow">
          <span class="name" title={t.id}>{t.name}</span>
          {#if t.time}<span class="time">{t.time} / {t.limit}</span>{/if}
          <span class="meta">{t.area}{#if t.area && t.prs.length}{' · '}{/if}{#each t.prs as n, i}{#if i}{', '}{/if}<a href={prHash(roomId, n)}>#{n}</a>{/each}</span>
          {#if onissue}<button class="go" onclick={() => openIssue(t)}>Issue ›</button>{/if}
        </div>
      {/each}
    {:else if first?.label}
      {#if first.body}<p class="text handoff">{first.body}</p>{/if}
      {#if first.attachments?.length}<PostFiles {roomId} post={first} onopen={onopenfile} />{/if}
    {:else}
    {#each sameChanges(notes) as g (g.key)}
      <div class="one">
        {#if notes.length > 1}<b>{g.targets.join(', ')}</b>{/if}
        {#each g.lines as line}<p>{line}</p>{:else}<p>Standing instructions changed</p>{/each}
        <details class="full"><summary>Show full instructions</summary>
          {#each g.notes as n (n.id)}{#if g.notes.length > 1}<b class="who">{n.target}</b>{/if}<p class="text">{fullText(n.text)}</p>{/each}
        </details>
      </div>
    {/each}
    {/if}
  </div>
</details>

<style>
  .note { margin: 6px 16px 6px 56px; font-size: var(--t-sm); color: var(--faint); }
  summary { list-style: none; cursor: pointer; display: flex; align-items: center; gap: 8px; min-width: 0; }
  summary::-webkit-details-marker { display: none; }
  .mark { color: var(--iris); font-size: var(--t-cap); width: 16px; text-align: center; flex: none; }
  .label { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; transition: color var(--motion-fast) var(--ease); }
  time { flex: none; font-size: var(--t-cap); }
  .chev { flex: none; transition: transform var(--motion-fast) var(--ease); }
  .note[open] > summary .chev { transform: rotate(90deg); }
  @media (hover: hover) { summary:hover .label { color: var(--ink-2); } }
  .panel { margin: 6px 0 2px 24px; padding: 2px 0 2px 12px; border-left: 2px solid var(--line); }
  .handoff { white-space: pre-wrap; color: var(--ink-2); line-height: 1.5; margin: 0; }
  .slow { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 2px var(--s3); padding: 6px 0; }
  .slow + .slow { border-top: 1px solid var(--hair); }
  .slow .name { font: var(--t-sm) var(--mono); color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .slow .time { color: var(--coral); text-align: right; }
  .slow .meta a { color: var(--iris); }
  .slow .go { grid-column: 2; color: var(--iris); font-weight: 600; font-size: var(--t-sm); text-align: right; }
  .one + .one { margin-top: 10px; }
  .one b { display: block; color: var(--muted); font-weight: 600; margin-bottom: 2px; }
  .one p { margin: 0; color: var(--ink-2); line-height: 1.5; }
  .full { margin-top: 4px; }
  .full summary { color: var(--faint); font-size: var(--t-sm); width: fit-content; }
  .full .text { margin-top: 4px; white-space: pre-wrap; color: var(--muted); font-size: var(--t-sm); }
  .full .who { margin-top: 8px; }
  @media (max-width: 700px) { .note { margin-left: 16px; } }
</style>
