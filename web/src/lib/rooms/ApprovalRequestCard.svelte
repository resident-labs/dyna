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
  import EventRow from './EventRow.svelte'
  import Card from '../ui/Card.svelte'

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
  let displayWhat = $derived(what.replace(/(--body-file(?:=|\s+))(?:'[^']*'|"[^"]*"|\S+)/g, '$1[body below]'))
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

{#snippet requestDetails()}
  <p><code>{displayWhat}</code>{#if where}{' '}in <code>{where}</code>{/if}</p>
  {#if asked?.title}<p>{asked.title}</p>{/if}
  {#if asked?.body}<pre class="body">{asked.body}</pre>{/if}
  {#if displayWhat !== what}<button class="btn" onclick={() => navigator.clipboard.writeText(what)}>Copy details</button>{/if}
{/snippet}

{#if waiting && asked}
  <div class="request" id="approval-{asked.requestId}">
   <Card>
    <p class="who"><b>{asked.requester}</b> asks to run <code>{displayWhat}</code>{#if where}{' '}in <code>{where}</code>{/if}</p>
    {#if asked.title}<p class="title">{asked.title}</p>{/if}
    {#if asked.body}<pre class="body">{asked.body}</pre>{/if}
    <p class="meta">Nothing runs until you approve. Dyna runs exactly this text.</p>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    {#if denying}
      <div class="row">
        <input bind:value={reason} placeholder="Reason for denying" aria-label="Reason for denying" onkeydown={(e) => { if (e.key === 'Enter' && reason.trim()) decide('deny') }} />
        <button class="btn" onclick={() => decide('deny')} disabled={busy || !reason.trim()}>Deny</button>
        <button class="btn" onclick={() => (denying = false)}>Cancel</button>
        {#if displayWhat !== what}<button class="btn" onclick={() => navigator.clipboard.writeText(what)}>Copy details</button>{/if}
      </div>
    {:else}
      <div class="row">
        <button class="btn fill" onclick={() => decide('approve')} disabled={busy}>Approve</button>
        <button class="btn" onclick={() => (denying = true)} disabled={busy}>Deny…</button>
        {#if displayWhat !== what}<button class="btn" onclick={() => navigator.clipboard.writeText(what)}>Copy details</button>{/if}
      </div>
    {/if}
   </Card>
  </div>
{:else if asked}
  <div id="approval-{asked.requestId}">
  <EventRow id={post.id} ts={post.ts} {surface} title="{asked.requester} requested a write" {chips} detail={requestDetails}>
    {#snippet icon()}<span aria-hidden="true">?</span>{/snippet}
  </EventRow>
  </div>
{/if}

<style>
  .request { margin: var(--s2) var(--s4); border-left: 3px solid var(--tone-warning); }
  .who { margin: 0; color: var(--ink); }
  .who b { color: var(--gold); font-weight: 600; }
  code { font: var(--t-sm) var(--mono, ui-monospace, monospace); }
  .title { margin: 0; color: var(--ink); font-weight: 600; overflow-wrap: anywhere; }
  .body { margin: 0; padding: 6px var(--s3); font: var(--t-sm)/1.5 var(--mono, ui-monospace, monospace); background: #19181a; color: var(--ink); border-radius: 6px; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 320px; overflow: auto; }
  .meta { margin: 0; font-size: var(--t-sm); color: var(--muted); }
  .err { margin: 0; font-size: var(--t-sm); color: var(--coral); }
  .row { display: flex; gap: var(--s2); flex-wrap: wrap; align-items: center; }
  input { flex: 1; min-width: 160px; padding: 6px var(--s3); border-radius: 6px; background: var(--raise); color: var(--ink); border: 1px solid var(--line-soft); font-size: var(--t-ui); }
  @media (max-width: 760px) { input { min-height: 44px; } }
</style>
