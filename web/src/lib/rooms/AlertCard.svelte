<script lang="ts">
  // The card for an alert: a doctor report or the engine stuck read-only.
  // Something needs the owner's seat now, so the card has a coral edge, the
  // ranked causes with their numbers, and one command to run next as a chip
  // the card copies. It reads the typed payload of the post, never its text;
  // the raw report stays the agent's evidence behind Details in a DM.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { alertEvent } from './eventPayloads.ts'
  import type { AlertSource } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const sourceNames: Record<AlertSource, string> = { doctor: 'Doctor report', engine: 'Engine alert' }

  let payload = $derived(alertEvent(post))
  let title = $derived.by(() => {
    const p = payload
    if (!p) return 'Alert'
    const name = sourceNames[p.source] ?? 'Alert'
    return p.reportId ? `${name} ${p.reportId}` : name
  })
  // The doctor names its next command; the engine names the one remedy.
  let command = $derived(payload?.nextCommand || payload?.remedy || '')
  let commandLabel = $derived(payload?.nextCommand ? 'Next' : 'Remedy')
  // The summary repeats the first finding's cause on a doctor report, so it
  // shows only when it adds a sentence of its own.
  let summary = $derived(payload?.summary ?? '')
  let showSummary = $derived(!!summary && !(payload?.findings?.[0]?.cause ?? '').startsWith(summary))
  let copyState = $state<'' | 'copied' | 'failed'>('')

  async function copyCommand() {
    if (!command || copyState === 'copied') return
    try {
      await navigator.clipboard.writeText(command)
      copyState = 'copied'
    } catch {
      copyState = 'failed'
    }
  }
</script>

<div class="card alert {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">!</span>
    <span class="title">{title}</span>
    {#if payload?.trigger}<span class="fact">{payload.trigger}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if showSummary}<p class="summary">{summary}</p>{/if}
  {#if payload?.findings?.length}
    <ul class="findings">
      {#each payload.findings as finding, i (i)}
        <li class={finding.severity}>
          <span class="dot" aria-hidden="true"></span>
          <span class="cause"><b>{finding.title}</b>{#if finding.cause}<span class="evidence">{finding.cause}</span>{/if}</span>
        </li>
      {/each}
    </ul>
  {/if}
  {#if command}
    <div class="step">
      <span class="step-label">{commandLabel}</span>
      <code class="command">{command}</code>
      <button class="btn" type="button" onclick={copyCommand}>Copy</button>
      <span class="status" role="status">{copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed. Try again.' : ''}</span>
    </div>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    border-left: 3px solid var(--coral);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum { display: flex; align-items: center; gap: 8px; padding: 8px 12px; color: var(--ink-2); }
  .glyph {
    flex: none;
    width: 16px;
    height: 16px;
    line-height: 16px;
    text-align: center;
    border-radius: 50%;
    background: var(--coral);
    color: var(--main);
    font-size: var(--t-cap);
    font-weight: 700;
  }
  .title { font-weight: 600; flex: none; }
  .fact { color: var(--muted); font-size: var(--t-sm); }
  time { margin-left: auto; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .summary { margin: 0; padding: 0 12px 6px; color: var(--ink-2); }
  .findings { margin: 0; padding: 0 12px 8px; list-style: none; display: grid; gap: 6px; }
  .findings li { display: flex; gap: 8px; align-items: baseline; }
  .dot { flex: none; width: 6px; height: 6px; border-radius: 50%; transform: translateY(-2px); background: var(--gold); }
  .findings li.critical .dot { background: var(--coral); }
  .cause { display: flex; flex-wrap: wrap; gap: 4px 8px; min-width: 0; color: var(--ink-2); }
  .cause b { font-weight: 600; }
  .evidence { color: var(--muted); font-size: var(--t-sm); min-width: 0; overflow-wrap: anywhere; }
  .step {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px 10px;
    border-top: 1px solid var(--hair);
    flex-wrap: wrap;
  }
  .step-label { color: var(--faint); font-size: var(--t-cap); flex: none; }
  .command {
    flex: 1 1 auto;
    min-width: 0;
    padding: 3px 8px;
    border-radius: var(--r-sm);
    background: var(--main);
    color: var(--ink-2);
    font: var(--t-sm)/1.5 var(--mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .status { color: var(--faint); font-size: var(--t-cap); }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2); }
    time { display: none; }
    .summary, .findings, .step { padding-inline: var(--s2); }
    .command { white-space: normal; overflow-wrap: anywhere; }
  }
</style>
