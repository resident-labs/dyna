<script lang="ts">
  import { dialog as modal } from '../ui/dialog.ts'
  import { sessionIds } from './consolidation.ts'
  import { bulkTitles, bulkRetryable, bulkResultText, adoptTargets, type BulkAction, type BulkValue } from './bulkSessions.ts'
  import type { Member } from '../api/room.ts'

  interface Props {
    batch: { key: number; roomId: string; action: BulkAction; sessions: Member[]; value: BulkValue }
    members?: Member[]
    onclose: () => void
    onrun: (force: boolean) => void
    onconfigure: (patch: { profile?: string; parent?: string }) => void
    onreload: () => void
    onselectfailed: () => void
  }
  // members: the room's sessions, for the Move under… parent picker.
  let { batch, members = [], onclose, onrun, onconfigure, onreload, onselectfailed }: Props = $props()
  let close = $state<HTMLButtonElement>(), confirmForce = $state(false)
  let value = $derived(batch.value)
  let retries = $derived(value.results.filter((r) => bulkRetryable(r, batch.action)))
  let accepted = $derived(value.results.filter((r) => r.ok).length)
  let targets = $derived(batch.action === 'adopt' ? adoptTargets(members, sessionIds(batch.sessions), value.maxDepth) : [])
  let tracked = $derived(batch.action === 'close' && retries.some((r) => /responsibilities:|tracked|pull request/i.test(r.error ?? '')))
  const name = (id: string) => batch.sessions.find((m) => m.id === id)?.name ?? id
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="card" role="dialog" aria-modal="true" aria-label={bulkTitles[batch.action]} tabindex="-1" use:modal={{ close: onclose, initial: () => close }} onclick={(e) => e.stopPropagation()}>
    <header><div><h2>{bulkTitles[batch.action]}</h2><p>{batch.sessions.length} selected sessions</p></div><button class="close" bind:this={close} aria-label="Close" onclick={onclose}>×</button></header>
    {#if value.error}<p class="error" role="alert">{value.error}</p>{/if}
    {#if !value.started}
      {#if batch.action === 'transfer'}
        <p class="hint">Choose one model. Each agent keeps its own session and gets up to 30 seconds to hand off.</p>
        {#if value.loading}<p class="hint">Loading models…</p>{:else if !value.profiles.length && !value.error}<p class="error">No models are available. Add a model in Settings.</p>{/if}
        <fieldset disabled={value.loading || value.busy} class="models"><legend>Model for all selected agents</legend>
          {#each value.profiles as p (p.name)}<label class="model" class:current={value.profile === p.name}><input type="radio" name="bulk-model" checked={value.profile === p.name} onchange={() => onconfigure({ profile: p.name })} /><span><strong>{p.name}</strong>{#if p.model}<small>{p.model}</small>{/if}</span></label>{/each}
        </fieldset>
        {#if value.error}<button class="quiet" onclick={onreload}>Load models again</button>{/if}
      {:else if batch.action === 'adopt'}
        <fieldset disabled={value.loading || value.busy} class="models"><legend>New parent for all selected sessions</legend>
          {#each targets as t (t.member.id)}<label class="model" class:current={value.parent === t.member.id} class:off={t.reason}><input type="radio" name="bulk-parent" disabled={!!t.reason} checked={value.parent === t.member.id} onchange={() => onconfigure({ parent: t.member.id })} /><span><strong>{t.member.name}</strong>{#if t.reason}<small>{t.reason}</small>{/if}</span></label>{/each}
        </fieldset>
      {:else if batch.action === 'close'}<p class="hint">Close all selected sessions? PR authorship and held goals move to the nearest open ancestor, or root. Reviewer roles return to the pool. Live workflows are canceled.</p>
      {:else}<p class="hint">Apply this action to all selected sessions.</p>{/if}
      <p class="names">{batch.sessions.map((m) => m.name).join(', ')}</p>
    {:else}
      <p class="progress" aria-live="polite">{accepted} accepted{retries.length ? ` · ${retries.length} need attention` : ''}{value.busy ? ' · Working…' : ''}</p>
      {#if batch.action === 'transfer'}<p class="hint">Model: {value.profile}</p><p class="hint">Accepted transfers continue in the background. Progress below follows each saved transfer.</p>{/if}
      <ul class="results">{#each value.results as result (result.session)}<li class:error={bulkRetryable(result, batch.action)}><strong>{name(result.session)}</strong><span>{bulkResultText(result, batch.action)}</span>{#if result.transfer?.error}<span>{result.transfer.error}</span>{/if}{#if result.progressError}<span>{result.progressError}</span>{/if}</li>{/each}</ul>
    {/if}
    {#if confirmForce}<p class="error">Force close the remaining {retries.length} sessions? Work that could not move may remain unresolved. Review the refusals above first.</p>{/if}
    <footer>
      <button class="quiet" onclick={onclose}>{value.busy ? 'Hide progress' : value.started ? 'Done' : 'Cancel'}</button>
      {#if !value.started}<button class="go" class:danger={batch.action === 'close' || batch.action === 'stop'} disabled={value.loading || value.busy || batch.action === 'transfer' && !value.profile || batch.action === 'adopt' && !value.parent} onclick={() => onrun(false)}>{{ stop: 'Stop', transfer: 'Transfer', close: 'Close sessions', promote: 'Promote', adopt: 'Move' }[batch.action]}</button>
      {:else if !value.busy && retries.length}<button class="quiet" onclick={onselectfailed}>Select refusals</button><button class="go" onclick={() => { confirmForce = false; onrun(false) }}>Retry remaining</button>{/if}
      {#if tracked && !value.busy}{#if confirmForce}<button class="go danger" onclick={() => { confirmForce = false; onrun(true) }}>Confirm force close</button>{:else}<button class="quiet" onclick={() => (confirmForce = true)}>Force close remaining…</button>{/if}{/if}
    </footer>
  </div>
</div>
<style>
  .backdrop { position: fixed; inset: 0; z-index: 62; padding: 20px; background: #08080aaa; display: grid; place-items: center; }
  .card { width: min(440px, 100%); max-height: calc(100dvh - 40px); overflow-y: auto; border-radius: 12px; background: var(--side); color: var(--ink); padding: 22px; box-shadow: 0 24px 64px #0008; outline: none; }
  header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--s3); }
  header > div { min-width: 0; }
  h2 { margin: 0; font-size: var(--t-head); overflow-wrap: anywhere; }
  header p { margin: 5px 0 20px; color: var(--muted); font-size: var(--t-ui); }
  .close { font-size: var(--t-display); min-width: 32px; min-height: 32px; }
  .hint, .names { font-size: var(--t-ui); color: var(--muted); line-height: var(--leading); overflow-wrap: anywhere; }
  fieldset { border: 0; padding: 0; margin: var(--s3) 0; }
  legend { font-size: var(--t-sm); color: var(--muted); margin-bottom: var(--s2); }
  .models { display: grid; gap: 6px; }
  .model { display: flex; align-items: center; gap: var(--s3); padding: 10px 14px; min-height: 58px; border: 1px solid var(--line); border-radius: 8px; background: var(--main); cursor: pointer; }
  .model.current { background: var(--iris-wash); border-color: var(--iris-edge); }
  .model.off { opacity: .55; cursor: default; }
  input { accent-color: var(--iris); }
  .model strong { font-size: var(--t-body); }
  small { display: block; color: var(--muted); font-size: var(--t-sm); margin-top: 3px; }
  .progress { color: var(--iris); font-size: var(--t-body); }
  .error { color: var(--coral); font-size: var(--t-ui); overflow-wrap: anywhere; }
  .results { padding: 0; list-style: none; }
  .results li { display: grid; gap: 3px; padding: var(--s2) 0; font-size: var(--t-ui); overflow-wrap: anywhere; }
  .results strong { font-size: var(--t-body); }
  footer { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--s2); margin-top: var(--s4); }
  footer button, .quiet { padding: var(--s2) var(--s3); border-radius: 6px; font-size: var(--t-ui); }
  .quiet { color: var(--muted); background: var(--hover); }
  .go { color: #18171a; background: var(--iris); font-weight: 600; }
  .go.danger { color: var(--coral); background: var(--hover); }
  button:disabled { opacity: .55; cursor: default; }
  @media (max-width: 480px) { .backdrop { padding: var(--s4); } .card { padding: 18px; max-height: calc(100dvh - 32px); } }
</style>
