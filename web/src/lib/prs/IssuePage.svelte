<script lang="ts">
  // One open issue: the title with its state, the facts line (FactsLine, as
  // on a pull request), the "Work on this issue" card with the working
  // session and the linked PRs, then the body and comments as the same
  // timeline a PR's conversation uses (Thread).
  //
  // Props:
  //   it         the issue row, or its GitHubView answer when loaded
  //   detail     the GitHubView answer, null while it loads; detailError
  //   onmention  adds the issue to the room composer
  //   onsession  opens the working session, or null; onpr opens a linked PR
  //   onback     the back link, on a narrow screen only
  import { ago } from '../ui/format.ts'
  import Card from '../ui/Card.svelte'
  import StatusPill from '../ui/StatusPill.svelte'
  import FactsLine from './FactsLine.svelte'
  import MentionInChat from './MentionInChat.svelte'
  import PRGlyph from './PRGlyph.svelte'
  import Thread from './Thread.svelte'
  import { itemState, sortThread, timeline } from './github.ts'
  import { openExternal } from '../../api.ts'
  import type { Reference } from '../rooms/references.ts'
  import type { GitHubDetail, GitHubItem } from '../api/github.ts'

  interface Props {
    it: GitHubItem
    detail?: GitHubDetail | null
    detailError?: string
    onmention?: (reference: Reference) => void
    onsession?: (() => void) | null
    onpr: (number: number) => void
    onback?: (() => void) | null
  }
  let { it, detail = null, detailError = '', onmention, onsession = null, onpr, onback = null }: Props = $props()
  let st = $derived(itemState('issue', it))
  let prs = $derived(it.linkedPRs ?? [])
  let thread = $derived(detail ? sortThread(detail) : [])
</script>

<div class="detail">
  {#if onback}
    <button class="back" onclick={onback}>
      <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M12.5 4.5 7 10l5.5 5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
      Issues
    </button>
  {/if}
  <header class="head">
    <h1>{it.title} <span class="num">#{it.number}</span></h1>
    <p class="meta">
      <span class="badge {st.tone}"><PRGlyph kind="issue" {it} size={14} />{st.label}</span>
      <span>Opened {ago(it.createdAt)}</span>
    </p>
    <FactsLine author={it.author?.login} labels={it.labels ?? []}>
      {#if it.assignees?.length}<span class="part">Assignees <span class="who">{it.assignees.map((a) => a.login).join(', ')}</span></span>{/if}
      {#each prs as p (p.number)}<span class="part">PR <button class="who" onclick={() => onpr(p.number)}>#{p.number}{p.isDraft ? ' draft' : ''}</button></span>{/each}
    </FactsLine>
  </header>
  <Card title="Work on this issue" count={(it.workingSession ? 1 : 0) + prs.length}>
    {#if it.workingSession}
      <div class="link">
        {#if onsession}<button class="name" onclick={onsession}>Session {it.workingSessionName || it.workingSession}</button>{:else}<span class="name">Session {it.workingSessionName || it.workingSession}</span>{/if}
        {#if it.workingSessionLive}<StatusPill tone="live" dot label="Working" />{:else}<span class="state">Not running</span>{/if}
      </div>
    {/if}
    {#each prs as p (p.number)}
      <div class="link">
        <button class="name" onclick={() => onpr(p.number)}>PR #{p.number}</button>
        <span class="state">{p.isDraft ? 'Draft' : p.state[0] + p.state.slice(1).toLowerCase()}</span>
      </div>
    {/each}
    {#if it.fixPR}
      <div class="link">
        <button class="name" onclick={() => onpr(it.fixPR ?? 0)}>Fix in #{it.fixPR}</button>
        <span class="state">Open</span>
      </div>
    {/if}
    {#if !it.workingSession && !prs.length && !it.fixPR}<p class="quiet">No agent is working on this yet.</p>{/if}
    <div class="acts">
      {#if onmention}<MentionInChat onmention={() => onmention?.({ kind: 'issue', number: it.number, title: it.title, state: it.state, url: it.url })} />{/if}
      <button class="act" onclick={() => openExternal(it.url)}>Open on GitHub</button>
    </div>
  </Card>
  <h3 class="conv">Conversation <span>{thread.length + 1}</span></h3>
  {#if detailError}
    <p class="err">{detailError}</p>
  {:else if detail}
    <Thread {detail} events={timeline(thread, [])} />
  {:else}
    <p class="quiet">Loading #{it.number}…</p>
  {/if}
</div>

<style>
  .detail { padding: var(--s4) var(--s5) var(--s6); min-width: 0; display: flex; flex-direction: column; gap: var(--s4); }
  .back { align-self: flex-start; display: inline-flex; align-items: center; gap: 4px; margin: 0 0 calc(var(--s2) * -1) -6px; padding: 6px; font-size: 13px; font-weight: 600; color: var(--muted); border-radius: 6px; }
  .back:hover { color: var(--ink); background: var(--hover); }
  h1 { margin: 0; font-size: 22px; line-height: 1.25; font-weight: 700; overflow-wrap: anywhere; }
  h1 .num { color: var(--faint); font-weight: 500; }
  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2) var(--s3); margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--faint); }
  .badge { display: inline-flex; align-items: center; gap: 5px; font-weight: 600; font-size: 12px; padding: 2px 9px 2px 7px; border-radius: 11px; }
  .badge :global(.glyph) { margin: 0; }
  .badge.sage { color: var(--sage); background: rgba(151, 201, 149, 0.13); }
  .badge.iris { color: var(--iris); background: var(--iris-wash); }
  .badge.coral { color: var(--coral); background: rgba(239, 138, 128, 0.13); }
  .badge.faint { color: var(--muted); background: var(--raise); }
  .link { display: flex; align-items: center; gap: var(--s3); justify-content: space-between; padding: var(--s1) 0; }
  .name { font-size: var(--t-body); color: var(--ink); text-align: left; overflow-wrap: anywhere; }
  button.name:hover { text-decoration: underline; }
  .acts { display: flex; flex-wrap: wrap; gap: 6px; padding-top: var(--s2); border-top: 1px solid var(--line-soft); }
  .act { font-size: 12.5px; font-weight: 600; color: var(--ink-2); padding: 6px 11px; border-radius: 6px; background: var(--raise); white-space: nowrap; }
  .act:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .act.primary { color: var(--rail); background: var(--iris); }
  .act:disabled { opacity: 0.5; }
  .conv { margin: 0; padding-bottom: 6px; border-bottom: 1px solid var(--line-soft); font-size: 13px; font-weight: 600; color: var(--ink); }
  .conv span { font-size: 11.5px; color: var(--faint); font-weight: 500; margin-left: 4px; }
  .quiet { color: var(--faint); font-size: 13.5px; margin: 0; }
  .state { color: var(--muted); font-size: var(--t-sm); }
  .err { color: var(--coral); overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    h1 { font-size: 18px; }
    .detail { padding: var(--s3) var(--s3) var(--s5); }
    .back, .act { min-height: 44px; }
    .name { min-height: 44px; }
  }
</style>
