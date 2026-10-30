<script lang="ts">
  // The row for how a held write ended: its state, exit code and the URL it
  // made. Output lines sit behind the row's own toggle, collapsed, so the feed
  // shows facts. It reads the typed payload, never the text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { approvalResultEvent } from './eventPayloads.ts'
  import type { ApprovalState } from './events.gen.ts'
  import EventRow from './EventRow.svelte'
  import StatusPill from '../ui/StatusPill.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const words: Record<ApprovalState, string> = { done: 'ran', failed: 'failed', denied: 'denied', expired: 'expired', cancelled: 'cancelled' }

  let result = $derived(approvalResultEvent(post))
  let output = $derived(result?.output ?? [])
  let open = $state(false)
  // A failed write opens by itself: its output is why the reader is here.
  let shown = $derived(open || result?.state === 'failed')
  let failed = $derived(result?.state === 'failed')
  let url = $derived(result?.url && /^https?:\/\//i.test(result.url) ? result.url : '')
  let outputText = $derived(output.join('\n'))
  let parts = $derived(url ? outputText.split(url) : [outputText])
</script>

{#snippet body()}
  {#if output.length}<pre class="output">{parts.join('')}</pre>{/if}
{/snippet}

{#snippet resultLink()}<a href={url} target="_blank" rel="noopener noreferrer">{url}</a>{/snippet}

{#if result && result.state !== 'denied'}
  <EventRow
    id={post.id}
    ts={post.ts}
    {surface}
    title={`Approved write ${words[result.state]}`}
    chips={result.exitCode ? [{ label: `exit ${result.exitCode}` }] : []}
    summary={[result.reason ?? '', output.length ? `${output.length} output line${output.length === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')}
    summaryExtra={url ? resultLink : undefined}
    open={shown}
    ontoggle={(value) => (open = value)}
    detail={output.length ? body : undefined}
  >
    {#snippet icon()}<span aria-hidden="true">{failed ? '✕' : '✓'}</span>{/snippet}
    {#snippet headline()}{#if failed}<strong>Approved write</strong> <StatusPill tone="danger" label="failed" />{:else}<strong>{result.state === 'denied' ? 'Write not run' : `Approved write ${words[result.state]}`}</strong>{/if}{/snippet}
  </EventRow>
{/if}

<style>
  .output {
    margin: 0;
    padding: var(--s2) 12px;
    border-top: 1px solid var(--hair);
    max-height: 260px;
    overflow: auto;
    background: var(--rail);
    color: var(--ink-2);
    font: var(--t-cap)/1.5 var(--mono);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
