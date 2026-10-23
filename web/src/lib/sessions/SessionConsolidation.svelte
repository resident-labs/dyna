<script lang="ts">
  import { dialog as modal } from '../ui/dialog.ts'
  import { api } from '../../api.js'
  import { activeConsolidation, consolidationPhase, createConsolidationView, selectionKey, sessionIds, type ConsolidationValue } from './consolidation.ts'
  import type { Member } from '../api/room.ts'
  import type { ConsolidationState } from '../api/session.ts'

  interface Props {
    roomId: string
    sessions?: Member[]
    operationId?: string
    onclose: () => void
    onoperation?: (operation: ConsolidationState) => void
    onopen?: (id: string) => void
  }
  let { roomId, sessions = [], operationId = '', onclose, onoperation = () => {}, onopen = () => {} }: Props = $props()
  let view = $state<ConsolidationValue>({ loading: true, busy: false, operation: null, target: '', profile: '', profiles: [], error: '', modelError: '' })
  let closeButton = $state<HTMLButtonElement>()
  let controller: ReturnType<typeof createConsolidationView> | undefined
  let target = $derived(sessions.find((m) => m.id === (view.operation?.target ?? view.target)))
  let frozen = $derived(!!view.operation || view.busy || view.loading)
  let otherModels = $derived(view.profiles.filter((p) => p.name !== target?.profile || p.model && p.model !== target?.model))
  const name = (id: string) => sessions.find((m) => m.id === id)?.name ?? view.operation?.sources?.find((s) => s.session === id)?.address ?? id

  $effect(() => {
    const rid = roomId, operation = operationId
    const snapshot = sessions.map((m) => ({ ...m }))
    const key = selectionKey(rid, sessionIds(snapshot), operation)
    let alive = true
    let reported: ConsolidationState | null = null
    const same = () => alive && roomId === rid && selectionKey(roomId, sessionIds(sessions), operationId) === key
    const next = createConsolidationView(api, { roomId: rid, sessions: snapshot, operationId: operation }, (value) => {
      if (!same()) return
      view = value
      if (value.operation && value.operation !== reported) { reported = value.operation; onoperation(value.operation) }
    })
    controller = next
    next.load()
    const timer = setInterval(() => { if (same() && activeConsolidation(view.operation)) next.refresh() }, 1000)
    return () => { alive = false; next.destroy(); clearInterval(timer) }
  })
  function dismiss() { controller?.destroy(); onclose() }
  function openSession(id: string) { controller?.destroy(); onopen(id) }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="card" role="dialog" tabindex="-1" aria-modal="true" aria-label="Consolidate sessions" use:modal={{ close: dismiss, initial: () => closeButton }} onclick={(e) => e.stopPropagation()}>
    <header><div><h2>Consolidate sessions</h2><p>{view.operation ? 'Progress is saved on the host. You can close this screen.' : `Keep one agent. The other ${sessions.length - 1} will hand off their work and be archived.`}</p></div><button class="close" bind:this={closeButton} onclick={dismiss} aria-label="Close">×</button></header>
    {#if view.loading}<p class="hint" role="status">Checking saved work…</p>{/if}
    {#if view.error}<p class="error" role="alert">{view.error}</p><button class="retry" disabled={view.busy} onclick={() => view.operation ? controller?.refresh() : controller?.load()}>Check again</button>{/if}
    {#if view.operation}
      <p class="progress" class:error={view.operation.phase === 'failed'} aria-live="polite">{consolidationPhase(view.operation)}</p>
      <p class="hint">Keeping <strong>{name(view.operation.target)}</strong> · {view.operation.profile}{view.operation.model ? ` · ${view.operation.model}` : ''}</p>
      {#if view.operation.error}<p class="error" role="alert">{view.operation.error}</p>{/if}
      <ul class="sources">
        {#each view.operation.sources ?? [] as source (source.session)}
          <li><button class="source-link" onclick={() => openSession(source.session)}>{name(source.session)}</button><span class="hint">{source.source === 'cooperative' ? 'Handoff ready' : source.source === 'transcript' ? 'Using transcript' : source.phase}{source.error ? ` · ${source.error}` : ''}</span></li>
        {/each}
      </ul>
    {:else}
      <fieldset disabled={frozen}><legend>Agent to keep</legend>
        <div class="models">
          {#each sessions as m (m.id)}
            <label class="model" class:current={view.target === m.id}>
              <input type="radio" name="consolidation-target" checked={view.target === m.id} onchange={() => controller?.chooseTarget(m.id ?? '')} />
              <span class="label"><strong>{m.name}</strong><span class="detail">{m.profile}{m.model ? ` · ${m.model}` : ''}</span></span><span class="badge">{view.target === m.id ? 'Keep' : ''}</span>
            </label>
          {/each}
        </div>
      </fieldset>
      <fieldset disabled={frozen}><legend>Model</legend>
        <div class="models">
          <label class="model" class:current={!view.profile}><input type="radio" name="consolidation-model" checked={!view.profile} onchange={() => controller?.chooseProfile('')} /><span class="label"><strong>Keep current model</strong><span class="detail">{target?.profile}{target?.model ? ` · ${target.model}` : ''}</span></span></label>
          {#each otherModels as p (p.name)}
            <label class="model" class:current={view.profile === p.name}><input type="radio" name="consolidation-model" checked={view.profile === p.name} onchange={() => controller?.chooseProfile(p.name)} /><span class="label"><strong>{p.name}</strong>{#if p.model && p.model !== p.name}<span class="detail">{p.model}</span>{/if}</span></label>
          {/each}
        </div>
        {#if view.modelError}<p class="hint">{view.modelError}</p>{/if}
      </fieldset>
    {/if}
    <footer>
      {#if view.operation}
        {#if view.operation.cancelPending}<button class="go" disabled={view.busy} onclick={() => controller?.resume()}>Finish cancellation</button>
        {:else}
        {#if ['waiting', 'failed'].includes(view.operation.phase)}<button class="quiet" disabled={view.busy} onclick={() => controller?.cancel()}>Cancel consolidation</button>{/if}
        {#if view.operation.phase === 'failed' || activeConsolidation(view.operation) && view.operation.error}<button class="go" disabled={view.busy} onclick={() => controller?.resume()}>{view.busy ? 'Resuming…' : 'Retry consolidation'}</button>{/if}
        {/if}
        {#if view.operation.phase === 'completed'}<button class="go" onclick={() => openSession(view.operation?.target ?? '')}>Open kept agent</button>{/if}
      {:else}<button class="quiet" onclick={dismiss}>Cancel</button><button class="go" disabled={frozen || !!view.error || sessions.length < 2 || sessions.length > 16} onclick={() => controller?.start()}>{view.busy ? 'Starting…' : 'Consolidate'}</button>{/if}
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
  .close { font-size: var(--t-display); line-height: 1; min-width: 32px; min-height: 32px; border-radius: 6px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .close:hover { background: var(--hover); }
  }
  .close:active { background: var(--select); }
  fieldset { border: 0; padding: 0; margin: 0 0 18px; min-width: 0; }
  legend { color: var(--muted); font-size: var(--t-sm); font-weight: 600; margin-bottom: var(--s2); }
  .models { display: grid; gap: 6px; }
  .model { display: flex; align-items: center; justify-content: space-between; gap: var(--s3); width: 100%; text-align: left; min-height: 58px; padding: 10px 14px; border: 1px solid var(--line); border-radius: 8px; background: var(--main); cursor: pointer; transition: background-color var(--motion-fast) var(--ease), border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .model:hover { background: var(--hover); border-color: var(--iris-edge); }
  }
  .model:active { background: var(--select); }
  input { accent-color: var(--iris); flex: none; }
  .label { display: grid; gap: 2px; min-width: 0; flex: 1; }
  strong { font-size: var(--t-body); font-weight: 600; overflow-wrap: anywhere; }
  .detail { color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .badge { color: var(--iris); font-size: var(--t-sm); flex: none; }
  .current { background: transparent; }
  .progress { color: var(--iris); font-size: var(--t-body); margin: 0 0 var(--s3); }
  .error { color: var(--coral); font-size: var(--t-ui); overflow-wrap: anywhere; }
  .hint { color: var(--muted); font-size: var(--t-ui); overflow-wrap: anywhere; }
  .sources { list-style: none; padding: 0; margin: var(--s3) 0; }
  .source-link { color: var(--ink); font-size: var(--t-body); text-align: left; overflow-wrap: anywhere; }
  .source-link:hover { text-decoration: underline; }
  .sources li { display: grid; gap: 3px; margin: 10px 0; font-size: var(--t-body); overflow-wrap: anywhere; }
  footer { margin-top: var(--s4); display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--s2); }
  footer button, .retry { padding: var(--s2) var(--s3); border-radius: 6px; font-size: var(--t-ui); }
  .quiet { color: var(--muted); background: var(--hover); }
  .go { color: #18171a; background: var(--iris); font-weight: 600; }
  button:disabled, fieldset:disabled { opacity: .55; cursor: default; }
  @media (max-width: 480px) { .backdrop { padding: var(--s4); } .card { padding: 18px; max-height: calc(100dvh - 32px); } }
</style>
