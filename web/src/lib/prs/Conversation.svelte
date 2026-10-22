<script lang="ts">
  // The Conversation tab of a pull request: the Thread timeline with the
  // reviewer's rounds inline. The latest round opens its verdict and findings
  // (Verdict, ChangeReview); earlier rounds are one line each. While the latest
  // round has open findings, a card on top links to it. The merge box follows
  // the timeline.
  //
  // Props:
  //   detail, events, bots   as for Thread; detail is null for a local change
  //   change    the full change (rounds, findings, verdict), or null
  //   checks    the CI checks, for the CI line
  //   opener names who opened it (authorName)
  //   names, roomId, onchange   for Verdict and the replies in ChangeReview
  import { tick } from 'svelte'
  import { ago, markdown } from '../ui/format.js'
  import Card from '../ui/Card.svelte'
  import Chip from '../ui/Chip.svelte'
  import Thread from './Thread.svelte'
  import ChangeReview from './ChangeReview.svelte'
  import { checkSummary, verdictMeta, shortSha } from './pr.ts'
  import { openFindings, roundReview, ownerReview, firstSentence, type TimelineEntry } from './github.ts'
  import type { BotReview, ChangeDetail, ReviewRound } from '../api/change.ts'
  import type { CheckRow } from './pr.ts'
  import type { GitHubDetail } from '../api/github.ts'
  import type { Names } from './prlist.ts'

  interface Props {
    detail?: Pick<GitHubDetail, 'author' | 'createdAt' | 'body'> | null
    events?: TimelineEntry[]
    bots?: BotReview[] | null
    change?: Partial<ChangeDetail> | null
    checks?: CheckRow[]
    names?: Names
    opener?: string
    roomId?: string
    onchange?: () => void | Promise<void>
  }
  let { detail = null, events = [], bots = [], change = null, checks = [], names = {}, opener = '', roomId = '', onchange = () => {} }: Props = $props()
  let latest = $derived((change?.reviewRounds ?? []).reduce<ReviewRound | null>((a, r) => (!a || r.round > a.round ? r : a), null))
  let open = $derived(openFindings(latest).length)
  let ci = $derived.by(() => {
    const { state, total, passed, failed } = checkSummary(checks)
    return {
      none: null,
      failed: { tone: 'coral', icon: '✗', line: `CI failed · ${failed.length} of ${total} checks` },
      running: { tone: 'gold', icon: '●', line: `CI running · ${passed.length} of ${total} checks` },
      incomplete: { tone: 'gold', icon: '●', line: `CI incomplete · ${passed.length} of ${total} checks passed` },
      passed: { tone: 'sage', icon: '✓', line: `CI passed · ${passed.length} of ${total} checks` },
    }[state]
  })
  let shown = $state(new Set<number>()) // rounds whose full findings are open
  const toggle = (n: number) => (shown = new Set(shown.has(n) ? [...shown].filter((x) => x !== n) : [...shown, n]))
  const reviewer = $derived((change?.reviewerSession && names[change.reviewerSession]) || 'Reviewer')
  // reveal opens the latest round and scrolls to it, for the pinned card and for "Read the review".
  export async function reveal() {
    if (latest) shown = new Set([...shown, latest.round])
    await tick()
    document.getElementById('latest-round')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }
</script>

{#snippet round(r: ReviewRound)}
  {@const vm = verdictMeta(r.verdict)}
  {@const n = r.findings?.length ?? 0}
  {@const isLatest = r.round === latest?.round}
  {@const left = new Set(openFindings(r).map((f) => f.id))}
  <div class="it" id={isLatest ? 'latest-round' : undefined}>
    <span class="av"><Chip tone={vm?.tone === 'ok' ? 'sage' : 'gold'}>R{r.round}</Chip></span>
    <div class="rcard" class:ok={vm?.tone === 'ok'}>
      <header><strong>{reviewer}</strong> {vm ? vm.label.toLowerCase() : 'reviewed'} · round {r.round} · {n} {n === 1 ? 'finding' : 'findings'}{r.head ? ` · at ${shortSha(r.head)}` : ''} · {ago(r.at)}</header>
      <div class="rbody">
        {#if r.summary}
          {#if shown.has(r.round)}<div class="prose">{@html markdown(r.summary)}</div>{:else}<p class="first">{firstSentence(r.summary)}</p>{/if}
        {/if}
        {#if !shown.has(r.round)}
          <ul class="lines">
            {#each r.findings ?? [] as f (f.id)}
              <li class:done={!left.has(f.id)}><b>{f.severity}</b> · {f.file}{f.line ? `:${f.line}` : ''} — {f.why}</li>
            {/each}
          </ul>
        {/if}
        {#if n || r.summary}
          <button class="link" aria-expanded={shown.has(r.round)} onclick={() => toggle(r.round)}>{shown.has(r.round) ? 'Hide review' : 'Show review'}</button>
          {#if shown.has(r.round) && n}<ChangeReview {...roundReview(change, r, latest, roomId)} {onchange} />{/if}
        {/if}
      </div>
    </div>
  </div>
{/snippet}
{#snippet after()}
  {#if ci}<p class="it ev"><span class="av small"><Chip tone={ci.tone}>{ci.icon}</Chip></span>{ci.line}</p>{/if}
{/snippet}

{#if latest && open}
  <Card>
    <button class="pin" onclick={reveal}>
      <span class="chip"><Chip tone="gold">{`Round ${latest.round} · ${open} open`}</Chip></span>
      <span class="why">Findings the author has not settled</span>
      <span class="go">View ›</span>
    </button>
  </Card>
{/if}
{#if change?.findings?.some((f) => f.severity === 'owner')}
  <Card><ChangeReview {...ownerReview(change, roomId)} {onchange} /></Card>
{/if}
<Thread {detail} {opener} {events} {bots} {round} {after} />

<style>
  .pin { display: flex; align-items: center; gap: var(--s2); width: 100%; text-align: left; font-size: 13px; color: var(--ink-2); }
  .pin .chip, .pin .go { flex: none; }
  .pin .why { min-width: 0; }
  .pin .go { margin-left: auto; color: var(--iris); font-weight: 600; }
  .rcard { background: var(--side); border: 1px solid rgba(239, 138, 128, 0.35); border-radius: 10px; overflow: hidden; }
  .rcard.ok { border-color: rgba(151, 201, 149, 0.35); }
  .rcard > header { padding: 8px 12px; border-bottom: 1px solid var(--line-soft); font-size: 12.5px; color: var(--faint); }
  .rcard > header strong { color: var(--ink); }
  .rbody { padding: 4px 12px 10px; }
  .first { margin: 0; font-size: 13.5px; color: var(--ink-2); overflow-wrap: anywhere; }
  .rbody .prose { font-size: 13.5px; color: var(--ink-2); overflow-wrap: anywhere; }
  .lines { list-style: none; margin: 6px 0; padding: 0; font-size: 12.5px; color: var(--ink-2); }
  .lines li { padding: 2px 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lines li.done { color: var(--faint); text-decoration: line-through; }
  .lines b { font-weight: 600; }
  .link { font-size: 12.5px; color: var(--iris); font-weight: 600; padding: 0; }
  .link:hover { text-decoration: underline; }
</style>
