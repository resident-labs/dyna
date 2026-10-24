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
  import EventRow from './EventRow.svelte'

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
  let severity = $derived(payload?.findings?.some((finding) => finding.severity === 'critical') ? 'critical' : 'warning')
  let copyState = $state<'' | 'copied' | 'failed'>('')
  // An alert stored before the typed events carries no payload: the family
  // fallback still routes it here, so the card shows its stored line rather
  // than an empty title.
  let legacy = $derived(!payload)
  let legacyText = $derived(legacy ? (post.text ?? '').trim() : '')

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

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  {title}
  chips={payload ? [{ label: severity, tone: severity === 'critical' ? 'coral' : 'gold', failure: severity === 'critical' }] : []}
  summary={[payload?.trigger ?? '', showSummary ? summary : '', payload?.findings?.length ? `${payload.findings.length} finding${payload.findings.length === 1 ? '' : 's'}` : '', command ? `${commandLabel}: ${command}` : ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}!{/snippet}
  {#snippet detail()}
  {#if legacy}
    <!-- TODO: legacy/shim remove later — an alert stored before the typed
         events has only its line. Remove when that history ages out. -->
    <p class="summary legacy">{legacyText}</p>
  {/if}
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
  {/snippet}
</EventRow>

<style>
  .summary { margin: 0 0 6px; color: var(--ink-2); }
  .summary.legacy { white-space: pre-wrap; overflow-wrap: anywhere; color: var(--muted); }
  .findings { margin: 0 0 8px; padding: 0; list-style: none; display: grid; gap: 6px; }
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
    padding: 6px 0 0;
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
    .command { white-space: normal; overflow-wrap: anywhere; }
  }
</style>
