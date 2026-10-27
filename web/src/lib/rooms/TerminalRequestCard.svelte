<script lang="ts">
  // The feed's row for a terminal request. While it waits for the owner it is
  // a card with the reason and the exact command, "Review and run" (which
  // opens the review in the dock; nothing runs yet) and "Decline…". Afterwards
  // it is a quiet row, and the result row shows the verdict and exit code. It
  // reads the typed payload of the post, never the text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { Question } from '../api/room.ts'
  import { terminalDoneEvent, terminalRequestEvent } from './eventPayloads.ts'
  import { commandLine, waitsForOwner } from '../sessions/termRequest.ts'
  import { errorText } from '../errorText.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed', request = undefined, onreviewrequest = undefined, ondeclinerequest = undefined }: {
    post: EventPost
    notice?: EventNotice
    surface?: EventSurface
    request?: Question
    onreviewrequest?: (id: string) => void
    ondeclinerequest?: (id: string, reason: string) => Promise<unknown>
  } = $props()

  let filed = $derived(terminalRequestEvent(post))
  let done = $derived(terminalDoneEvent(post))
  let waiting = $derived(!!filed && !!request && waitsForOwner(request))
  let declining = $state(false)
  let reason = $state('')
  let busy = $state(false)
  let error = $state('')
  let id = $derived(done?.requestId ?? filed?.requestId ?? '')
  // Who and what; the ID stays in the icon's tooltip.
  let title = $derived(request?.askerName ? `${request.askerName}'s terminal request` : 'Terminal request')
  let summary = $derived(done ? [done.note ?? ''].filter(Boolean).join(' · ') : [filed?.argv?.join(' ') ?? '', filed?.reason ?? ''].filter(Boolean).join(' · '))
  let chips = $derived(done ? [{ label: done.verdict, failure: done.verdict === 'failed' || done.verdict === 'denied' }, ...(done.exit ? [{ label: `exit ${done.exit.code}`, failure: done.exit.code !== 0 }] : [])] : [])

  async function decline() {
    if (busy || !filed) return
    busy = true
    error = ''
    try {
      await ondeclinerequest?.(filed.requestId, reason.trim())
      declining = false
    } catch (e) { error = errorText(e) } finally { busy = false }
  }
</script>

{#snippet icon()}
  <span title="Request {id}" aria-hidden="true">&gt;_</span>
{/snippet}

{#if waiting && filed && request}
  <section class="card" aria-label="Terminal request from {request.askerName}">
    <p class="who"><b>{request.askerName}</b> needs you in a terminal</p>
    <p class="why">{filed.reason}</p>
    {#if filed.argv?.length}<pre class="cmd">{commandLine(filed.argv)}</pre>{:else}<p class="meta">A plain shell, no command.</p>{/if}
    <p class="meta">Runs in <code>{filed.cwd}</code> as you. Nothing runs until you click Run.</p>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    {#if declining}
      <div class="row">
        <input bind:value={reason} placeholder="Reason (optional)" aria-label="Reason for declining" onkeydown={(e) => { if (e.key === 'Enter') decline() }} />
        <button class="b" onclick={decline} disabled={busy}>Decline</button>
        <button class="b" onclick={() => (declining = false)}>Cancel</button>
      </div>
    {:else}
      <div class="row">
        <button class="b primary" onclick={() => onreviewrequest?.(filed.requestId)}>Review and run</button>
        <button class="b" onclick={() => (declining = true)}>Decline…</button>
      </div>
    {/if}
  </section>
{:else}
  <EventRow id={post.id} ts={post.ts} {surface} {title} {summary} {chips} {icon} />
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/if}

<style>
  .card { margin: var(--s2) var(--s4); padding: var(--s3) 14px; border: 1px solid var(--line-soft); border-left: 3px solid var(--gold); border-radius: 8px; background: var(--gold-wash); display: flex; flex-direction: column; gap: var(--s2); font-size: var(--t-ui); color: var(--ink-2); }
  .who { margin: 0; color: var(--ink); }
  .who b { color: var(--gold); font-weight: 600; }
  .why { margin: 0; color: var(--ink); }
  .cmd { margin: 0; padding: 6px var(--s3); font: var(--t-sm)/1.5 var(--mono, ui-monospace, monospace); background: #19181a; color: var(--ink); border-radius: 6px; white-space: pre-wrap; overflow-wrap: anywhere; }
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
