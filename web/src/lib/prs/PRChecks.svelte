<script lang="ts">
  // The checks of one change. A summary line and bar come first, then each
  // failed check open with a link to its full log. Running,
  // passed and skipped checks, and the bot reviews fold
  // into one line each, and every check stays reachable inside its fold,
  // grouped by workflow. Bots never block a merge. Each row opens its page
  // on GitHub.
  //
  // Props:
  //   checks  [{name, state, status, conclusion, url, commentUrl, failureExcerpt}]
  //   bots    [{bot, state, findings, url}]
  //   pr      the change, to link a check that has no page of its own
  //   main    the saved CI result of the default branch ({state, head, reason}) from the
  //           change's merge item, or null
  import { openExternal } from '../../api.js'
  import Chip from '../ui/Chip.svelte'
  import { checkMeta, checkBuckets, workflowGroups, isLocal, shortSha, checkTime, type CheckRow } from './pr.ts'
  import { botStateWord, type MergeItem, type Row } from './prlist.ts'
  import type { BotReview } from '../api/change.ts'

  interface Props {
    checks?: CheckRow[]
    bots?: BotReview[]
    pr?: Row | null
    main?: MergeItem['main'] | null
  }
  let { checks = [], bots = [], pr = null, main = null }: Props = $props()
  let local = $derived(isLocal(pr))
  let buckets = $derived(checkBuckets(checks))
  // The test report note is a warning about the whole change (a missing or
  // unreadable JUnit report, the test budget), not the output of one check.
  let note = $derived((pr?.ciTestReportNote ?? '').trim())
  // Main CI is informational. Only a passing batch trial permits publication.
  let mainRow = $derived.by(() => {
    const reason = main?.reason ? main.reason.charAt(0).toUpperCase() + main.reason.slice(1) : ''
    if (main?.state === 'paused') return { tone: 'gold', text: reason || 'Merging paused in this room' }
    if (main?.state !== 'pending' && main?.state !== 'failed') return null
    const pending = main.state === 'pending'
    const tone = pending ? 'run' : 'coral'
    if (!main.head) return { tone, text: reason || (pending ? 'Waiting for main branch CI; the batch trial must pass' : 'Main branch CI failed; the batch trial must pass') }
    return { tone, text: pending ? `main branch CI is running at ${shortSha(main.head)}; the batch trial must pass` : `main branch CI failed at ${shortSha(main.head)}; the batch trial must pass` }
  })
  let folds = $derived([
    { id: 'running', label: 'running', items: buckets.running, open: true },
    { id: 'passed', label: 'passed', items: buckets.passed },
    { id: 'skipped', label: 'skipped', items: buckets.other },
  ].filter((f) => f.items.length))
  const BAR = [['failed', 'coral'], ['running', 'gold'], ['passed', 'sage'], ['other', 'raise']] as const
  const WORDS = [['running', 'running'], ['passed', 'passed'], ['other', 'skipped']] as const
  let bar = $derived(BAR.filter(([k]) => buckets[k].length))
  let summary = $derived(WORDS.filter(([k]) => buckets[k].length).map(([k, w]) => `${buckets[k].length} ${w}`))

  // A check row opens its run, else the bot's latest comment (a check with
  // no details page, such as CodeRabbit), else the PR on GitHub.
  function checkLink(c: CheckRow) {
    if (c.url) return { href: c.url, title: 'Open this check on GitHub' }
    if (c.commentUrl) return { href: c.commentUrl, title: "Open the bot's comment" }
    if (!local && pr?.url) return { href: pr.url, title: 'Open the PR on GitHub' }
    return null
  }
  const names = (items: CheckRow[]) => items.map((c) => workflowGroups([c])[0]?.items[0]?.job ?? c.name).join(', ')
</script>

{#snippet open(c: { name?: string; bot?: string }, link: { href: string; title?: string } | null, label: string)}
  {#if link}<button class="clink" onclick={() => openExternal(link.href)} aria-label="{label}: {c.name ?? c.bot}" title={link.title ?? label}>
    <svg viewBox="0 0 20 20" width="13" height="13" aria-hidden="true"><path d="M11 4h5v5M16 4l-7 7M8 5H5v10h10v-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
  </button>{:else}<span class="clink" aria-hidden="true"></span>{/if}
{/snippet}

{#if checks.length}
  <div class="sum">
    <b class:bad={buckets.failed.length}>{buckets.failed.length ? `${buckets.failed.length} failed` : 'No failures'}</b>
    {#if summary.length}<span>{summary.join(' · ')}</span>{/if}
  </div>
  <div class="bar" aria-hidden="true">{#each bar as [k, tone]}<i class={tone} style="flex: {buckets[k].length}"></i>{/each}</div>

  {#each buckets.failed as c}
    {@const cm = checkMeta(c)}
    {@const link = checkLink(c)}
    <section class="fail">
      <header><b>{c.name}</b><Chip tone="coral">{cm.word}</Chip>{#if checkTime(c)}<span class="count">{checkTime(c)}</span>{/if}</header>
      {#if c.failureExcerpt}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex (The output region scrolls with the keyboard.) -->
        <pre tabindex="0" role="region" aria-label="Saved failure output: {c.name}">{c.failureExcerpt}</pre>
      {/if}
      {#if link}<div class="act"><button class="btn" onclick={() => openExternal(link.href)} title={link.title} aria-label="Open the full log: {c.name}">Open the full log</button></div>{/if}
    </section>
  {/each}

  {#if note}<p class="line-note note">{note}</p>{/if}

  {#each folds as f (f.id)}
    <details class="fold" open={f.open}>
      <summary>{f.items.length} {f.label} <span class="what">· {names(f.items)}</span></summary>
      {#each workflowGroups(f.items) as g}
        <h5>{g.workflow}</h5>
        <ul class="checks">
          {#each g.items as c}
            {@const cm = checkMeta(c)}
            <li>
              <span class="cdot {cm.tone}" aria-hidden="true"></span>
              <span class="cname">{c.job}</span>
              {#if checkTime(c)}<span class="count">{checkTime(c)}</span>{/if}
              <span class="cword {cm.tone}">{cm.word}</span>
              {@render open(c, checkLink(c), 'Open this check')}
            </li>
          {/each}
        </ul>
      {/each}
    </details>
  {/each}
{:else if !local && pr?.ci === 'absent'}
  <p class="line-note">This PR has no CI checks. Add a GitHub CI test workflow before merging.</p>
{:else if !local && (pr?.ci === 'pending' || !pr?.ci)}
  <p class="line-note">Waiting for CI to report.</p>
{:else}
  <p class="line-note">No checks yet.</p>
{/if}

{#if bots.length}
  <details class="fold">
    <summary>Bot reviews <span class="what">· {bots.map((b) => b.bot).join(', ')}, do not block</span></summary>
    <ul class="checks">
      {#each bots as b}
        <li>
          <span class="cname">{b.bot}</span>
          <span class="cword" class:gold={b.state === 'changes_requested'} class:sage={b.state === 'approved'}>{botStateWord(b.state)}</span>
          <span class="count">{b.findings ? `${b.findings} ${b.findings === 1 ? 'finding' : 'findings'}` : ''}</span>
          {@render open(b, b.url ? { href: b.url, title: 'Open the review on GitHub' } : null, `Open the ${b.bot} review`)}
        </li>
      {/each}
    </ul>
    <p class="line-note">Bots never block a merge.</p>
  </details>
{/if}
{#if mainRow}
  <div class="fold line"><span class="cdot {mainRow.tone}" aria-hidden="true"></span>{mainRow.text}</div>
{/if}

<style>
  .sum { display: flex; flex-wrap: wrap; gap: var(--s1) var(--s4); align-items: baseline; margin-top: var(--s3); }
  .sum b { font-size: var(--t-page); font-weight: 600; color: var(--sage); }
  .sum b.bad { color: var(--coral); }
  .sum span { color: var(--muted); font-size: var(--t-sm); }
  .bar { display: flex; height: 8px; gap: 2px; margin: var(--s2) 0 var(--s4); border-radius: 4px; overflow: hidden; }
  .bar i { display: block; }
  .bar .coral { background: var(--coral); }
  .bar .gold { background: var(--gold); }
  .bar .sage { background: var(--sage); }
  .bar .raise { background: var(--raise); }
  .fail { margin-bottom: var(--s3); border-radius: var(--r-sm); overflow: hidden; box-shadow: inset 0 0 0 1px rgba(239, 138, 128, 0.4); }
  .fail header { display: flex; align-items: center; gap: var(--s2); padding: var(--s3) var(--s4); background: rgba(239, 138, 128, 0.08); min-width: 0; }
  .fail header b { font-size: var(--t-body); font-weight: 600; overflow-wrap: anywhere; }
  .fail pre { max-height: 24em; overflow: auto; margin: 0; padding: var(--s3) var(--s4); font: 12px/1.5 var(--mono); color: var(--ink-2); white-space: pre-wrap; overflow-wrap: anywhere; background: var(--side); }
  .note { margin: 0 0 var(--s3); white-space: pre-wrap; overflow-wrap: anywhere; }
  .act { padding: var(--s2) var(--s3); }
  .btn { padding: 6px 14px; border-radius: var(--r-xs); font-size: var(--t-sm); font-weight: 600; background: var(--raise); color: var(--ink-2); }
  .btn:hover { color: var(--ink); background: var(--raise-hover); }
  .fold { margin-top: var(--s3); padding: var(--s2) var(--s3); border-radius: var(--r-sm); background: var(--side); color: var(--muted); font-size: var(--t-sm); }
  .fold summary { list-style: none; cursor: pointer; min-height: 28px; display: flex; align-items: center; gap: var(--s1); color: var(--ink-2); }
  .fold summary::-webkit-details-marker { display: none; }
  .fold summary::before { content: '▸'; color: var(--faint); flex: none; width: 1ch; }
  .fold[open] > summary::before { content: '▾'; }
  .fold.line { display: flex; align-items: center; gap: var(--s2); padding: var(--s3); }
  .what { color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  h5 { margin: var(--s3) 0 var(--s1); font-size: var(--t-cap); font-weight: 600; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; }
  .line-note { margin: var(--s2) 0; font-size: var(--t-sm); color: var(--muted); }
  .checks { list-style: none; margin: 0; padding: 0; }
  .checks li { display: flex; align-items: center; gap: 10px; padding: 2px 0; font-size: 13px; }
  .cdot { width: 8px; height: 8px; border-radius: 50%; background: var(--faint); flex: none; }
  .cdot.sage { background: var(--sage); }
  .cdot.coral { background: var(--coral); }
  .cdot.gold { background: var(--gold); }
  .cdot.run { background: var(--gold); animation: pulse 1.6s ease-in-out infinite; }
  .cname { flex: 1; min-width: 0; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cword { font-size: 12px; color: var(--faint); flex: none; }
  .cword.sage { color: var(--sage); }
  .cword.coral { color: var(--coral); }
  .cword.gold, .cword.run { color: var(--gold); }
  .count { font-size: 12px; color: var(--faint); flex: none; text-align: right; }
  .clink { width: 26px; height: 26px; display: grid; place-items: center; border-radius: 5px; color: var(--faint); flex: none; }
  button.clink:hover { color: var(--ink); background: var(--hover); }
  @keyframes pulse { 50% { opacity: 0.4; } }
  @media (max-width: 760px) {
    .clink { width: 44px; height: 44px; }
    .checks li { min-height: 44px; }
    .btn { min-height: 44px; }
  }
</style>
