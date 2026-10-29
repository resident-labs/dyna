<script lang="ts">
  // The feed's row for an approval request. While the request waits for the
  // owner it is a card with the repository, the command and the full title and
  // body the engine will post, attribution line included, and "Approve" and
  // "Deny…" (a denial needs a reason). Afterwards, or when another session
  // holds it, it is a quiet row. It reads the typed payload, never the text.
  import type { EventNotice } from '../api/room.ts'
  import type { ApprovalDecision, ApprovalOpen } from '../api/approvals.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { approvalRequestedEvent } from './eventPayloads.ts'
  import { errorText } from '../errorText.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed', approval = undefined, latest = true, ondecideapproval = undefined }: {
    post: EventPost
    notice?: EventNotice
    surface?: EventSurface
    approval?: ApprovalOpen
    latest?: boolean
    ondecideapproval?: (id: string, action: ApprovalDecision, reason: string) => Promise<unknown>
  } = $props()

  let asked = $derived(approvalRequestedEvent(post))
  let waiting = $derived(!!asked && approval?.at === 'owner' && latest)
  let denying = $state(false)
  let reason = $state('')
  let busy = $state(false)
  let error = $state('')
  let what = $derived(asked?.command ?? asked?.tool ?? 'a write')
  let where = $derived(asked?.repository ?? asked?.server ?? '')
  let chips = $derived(approval && !waiting && approval.at !== 'owner' ? [{ label: `waits on ${approval.at}` }] : [])

  async function decide(action: ApprovalDecision) {
    if (busy || !asked) return
    busy = true
    error = ''
    try {
      await ondecideapproval?.(asked.requestId, action, reason.trim())
      denying = false
    } catch (e) { error = errorText(e) } finally { busy = false }
  }
</script>

{#if waiting && asked}
  <section class="card" aria-label="Approval request from {asked.requester}">
    <p class="who"><b>{asked.requester}</b> asks to run <code>{what}</code>{#if where} in <code>{where}</code>{/if}</p>
    {#if asked.title}<p class="title">{asked.title}</p>{/if}
    {#if asked.body}<pre class="body">{asked.body}</pre>{/if}
    <p class="meta">Nothing runs until you approve. Dyna runs exactly this text.</p>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    {#if denying}
      <div class="row">
        <input bind:value={reason} placeholder="Reason for denying" aria-label="Reason for denying" onkeydown={(e) => { if (e.key === 'Enter' && reason.trim()) decide('deny') }} />
        <button class="b" onclick={() => decide('deny')} disabled={busy || !reason.trim()}>Deny</button>
        <button class="b" onclick={() => (denying = false)}>Cancel</button>
      </div>
    {:else}
      <div class="row">
        <button class="b primary" onclick={() => decide('approve')} disabled={busy}>Approve</button>
        <button class="b" onclick={() => (denying = true)} disabled={busy}>Deny…</button>
      </div>
    {/if}
  </section>
{:else if asked}
  <EventRow id={post.id} ts={post.ts} {surface} title="{asked.requester} asked to run {what}" summary={[where, asked.title ?? ''].filter(Boolean).join(' · ')} {chips}>
    {#snippet icon()}<span aria-hidden="true">?</span>{/snippet}
  </EventRow>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/if}

<style>
  .card { margin: var(--s2) var(--s4); padding: var(--s3) 14px; border: 1px solid var(--line-soft); border-left: 3px solid var(--gold); border-radius: 8px; background: var(--gold-wash); display: flex; flex-direction: column; gap: var(--s2); font-size: var(--t-ui); color: var(--ink-2); }
  .who { margin: 0; color: var(--ink); }
  .who b { color: var(--gold); font-weight: 600; }
  code { font: var(--t-sm) var(--mono, ui-monospace, monospace); }
  .title { margin: 0; color: var(--ink); font-weight: 600; overflow-wrap: anywhere; }
  .body { margin: 0; padding: 6px var(--s3); font: var(--t-sm)/1.5 var(--mono, ui-monospace, monospace); background: #19181a; color: var(--ink); border-radius: 6px; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 320px; overflow: auto; }
  .meta { margin: 0; font-size: var(--t-sm); color: var(--muted); }
  .err { margin: 0; font-size: var(--t-sm); color: var(--coral); }
  .row { display: flex; gap: var(--s2); flex-wrap: wrap; align-items: center; }
  input { flex: 1; min-width: 160px; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink); border: 1px solid var(--line-soft); font-size: var(--t-ui); }
  .b { font-size: var(--t-ui); font-weight: 600; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink-2); }
  .b:not(:disabled), .b.primary:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .b:hover:not(:disabled) { background: var(--raise-hover); color: var(--ink); }
  }
  .b:active:not(:disabled) { background: var(--select); }
  .b.primary { background: var(--iris); color: #18161d; }
  @media (hover: hover) {
    .b.primary:hover:not(:disabled) { background: var(--iris-hover); color: #18161d; }
  }
  .b.primary:active:not(:disabled) { background: var(--select); color: var(--ink); }
  .b:disabled { opacity: 0.6; }
  @media (max-width: 760px) {
    .b, input { min-height: 44px; }
  }
</style>
