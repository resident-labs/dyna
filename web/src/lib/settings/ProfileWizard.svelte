<script lang="ts">
  import { errorText } from '../errorText.ts'
  import { dialog as modal } from '../ui/dialog.js'
  import Loading from './Loading.svelte'
  import { onMount, tick, untrack } from 'svelte'
  import { api } from '../../api.js'
  import ProfileEditor from './ProfileEditor.svelte'
  import HarnessIcon from './harnesses/HarnessIcon.svelte'
  import UsageMeter from './usage/UsageMeter.svelte'
  import { accountStatus, observation, dateTime, requestGate } from './usage/state.ts'
  import { harnesses as knownHarnesses, filterModels, inCategory, routeModel, selectedAccount, blankProfile, normalizeModels, accessLabel, publisherIcon, withEffort, routeEffort, type EditableProfile, type EditorDraft, type ModelSelection } from './profiles/state.ts'
  import type { Catalog, CatalogModel, UsageAccount } from '../api/account.ts'
  import type { ProfileView } from '../api/profile.ts'

  interface Props {
    harnesses?: string[]
    // unready: harness ID → why a new profile cannot use it yet, such as "Install … first."
    unready?: Record<string, string>
    initialSelection?: ModelSelection | null
    onsaved: (profile: ProfileView) => void
    oncancel: () => void
    onusage?: (accountId: string) => void
  }
  let { harnesses = [], unready = {}, initialSelection = null, onsaved, oncancel, onusage }: Props = $props()
  // Read initialSelection once. Returning to this wizard must not reset a choice.
  const initial = untrack(() => initialSelection)
  let harness = $state(initial?.harness || 'opencode')
  let step = $state(initial?.harness ? 1 : 0)
  let category = $state((initial?.harness || 'opencode') === 'opencode' ? 'opencode' : 'all')
  let provider = $state('')
  let search = $state('')
  let catalog = $state<Catalog | null>(null)
  let catalogBusy = $state(false)
  let catalogError = $state('')
  let accounts = $state<UsageAccount[]>([])
  let accountBusy = $state(false)
  let accountError = $state('')
  let preselectionMessage = $state('')
  let connectionId = $state(initial?.connectionId || '')
  let model = $state<CatalogModel | null>(null)
  // exactId is a requested or saved model ID that names one effort of model,
  // such as an OpenCode #variant. The profile editor picks efforts.
  let exactId = $state('')
  let manual = $state(false)
  let manualId = $state(initial?.routeId || '')
  // Ordered fallbacks after the primary model: catalog models or exact ID strings.
  let fallbackModels = $state<(CatalogModel | string)[]>([])
  let fallbackPick = $state('')
  let fallbackManual = $state('')
  let editorProfile = $state<EditableProfile | null>(null)
  let profileDraft = $state<EditableProfile | null>(null)
  let editorDraft = $state<EditorDraft | null>(null)
  let heading = $state<HTMLElement>()
  const catalogGate = requestGate()
  const accountGate = requestGate()
  const iconIds = ['opencode', 'codex', 'claude', 'antigravity', 'muse', 'pi', 'grok']
  const choices = $derived([...knownHarnesses, ...harnesses.filter(id => !knownHarnesses.some(h => h.id === id) && !['custom', 'mock'].includes(id)).map(id => ({ id, label: id, description: 'Use an exact model ID or the harness default.', icon: '' }))])
  const selectedHarness = $derived(choices.find(h => h.id === harness) || { id: harness, label: harness === 'custom' ? 'Custom harness' : harness === 'mock' ? 'Mock harness' : harness, icon: '', description: '' })
  const models = $derived(catalog?.models ?? [])
  const filtered = $derived(filterModels(models, { category, provider, search }))
  const providers = $derived([...new Set(models.filter(m => inCategory(m, category)).map(m => m.publisher || m.providerId).filter((x): x is string => !!x))].sort())
  const primaryId = $derived(manual ? manualId.trim() : exactId || model?.id || '')
  const fallbackId = (m: CatalogModel | string) => typeof m === 'string' ? m : m.id
  const fallbackLabel = (m: CatalogModel | string) => typeof m === 'string' ? (models.find(x => x.id === m)?.label || m) : m.label || m.id
  const fallbackIds = $derived(fallbackModels.map(fallbackId))
  const fallbackChoices = $derived(models.filter(m => m.id !== primaryId && !fallbackIds.includes(m.id) && inCategory(m, category)))
  const account = $derived(selectedAccount(accounts, harness, model, connectionId))
  const editorHarnesses = $derived([...new Set([...harnesses, ...choices.map(h => h.id), 'custom', 'mock'])])

  async function focusHeading() { await tick(); heading?.focus() }
  async function loadAccounts(refresh = false) {
    const ticket = accountGate.next()
    accountBusy = true
    accountError = ''
    try {
      const result = await api.accountUsage(refresh)
      if (accountGate.current(ticket)) accounts = result.accounts ?? []
    } catch (e) {
      if (accountGate.current(ticket)) accountError = errorText(e)
    } finally {
      if (accountGate.current(ticket)) accountBusy = false
    }
  }
  async function loadModels(refresh = false, requestedRoute = '') {
    const ticket = catalogGate.next()
    const requestedHarness = harness
    catalogBusy = true
    catalogError = ''
    try {
      const result = await api.profileModels(requestedHarness, refresh)
      if (!catalogGate.current(ticket)) return
      catalog = result
      if (requestedRoute && step === 1) {
        const exact = routeModel(result.models ?? [], requestedRoute)
        const variant = exact ? null : routeEffort(result.models ?? [], requestedRoute)
        if (exact) chooseModel(exact)
        else if (variant) { chooseModel(variant.model); exactId = requestedRoute }
        else {
          manualId = requestedRoute
          preselectionMessage = `The requested route “${requestedRoute}” is not in this catalog. Enter it explicitly or choose another model.`
        }
      }
    } catch (e) {
      if (catalogGate.current(ticket)) catalogError = errorText(e)
    } finally {
      if (catalogGate.current(ticket)) catalogBusy = false
    }
  }
  // Another model or harness has other efforts and context window, so drop the
  // catalog's effort args and the compact field from both drafts before the
  // choice changes. The profile editor sets them again.
  function forgetKnobs() {
    const known = (catalog?.models ?? []).flatMap(m => m.efforts ?? [])
    if (profileDraft) profileDraft = { ...profileDraft, compact: null, extraArgs: withEffort(profileDraft.extraArgs ?? [], known, null) }
    if (editorDraft) editorDraft = { ...editorDraft, args: withEffort(lines(editorDraft.args ?? ''), known, null).join('\n') }
    exactId = ''
  }
  function chooseHarness(id: string) {
    if (profileDraft && id === harness) { step = 1; focusHeading(); return }
    if (id !== harness) forgetKnobs()
    catalogGate.invalidate()
    if (id !== harness) connectionId = ''
    harness = id
    category = id === 'opencode' ? 'opencode' : 'all'
    provider = ''; search = ''; catalog = null; model = null; manual = false; manualId = ''; preselectionMessage = ''
    fallbackModels = []; fallbackPick = ''; fallbackManual = ''
    step = 1
    focusHeading()
    if (!['custom', 'mock'].includes(id)) loadModels()
    else catalogBusy = false
  }
  function chooseModel(value: CatalogModel) { fallbackModels = fallbackModels.filter(m => fallbackId(m) !== value.id); if (value !== model) forgetKnobs(); model = value; manual = false; step = 2; focusHeading() }
  function enterManually() { manual = true; model = null; step = 2; focusHeading() }
  function addFallback(value: CatalogModel | string) {
    const id = (typeof value === 'string' ? value : value?.id ?? '').trim()
    if (!id || id === primaryId || fallbackIds.includes(id)) return
    fallbackModels = [...fallbackModels, typeof value === 'string' ? id : value]
  }
  function addPicked() { const m = models.find(x => x.id === fallbackPick); if (m) addFallback(m); fallbackPick = '' }
  function addManualFallback() { addFallback(fallbackManual); fallbackManual = '' }
  function moveFallback(i: number, by: number) {
    const j = i + by
    const a = fallbackModels[i], b = fallbackModels[j]
    if (a === undefined || b === undefined) return
    const next = [...fallbackModels]
    next[i] = b
    next[j] = a
    fallbackModels = next
  }
  function removeFallback(i: number) { fallbackModels = fallbackModels.filter((_, k) => k !== i) }
  const lines = (s: string) => s.split('\n').map(x => x.trim()).filter(Boolean)
  function settings() {
    const allModels = normalizeModels(primaryId, [primaryId, ...fallbackModels.map(fallbackId)])
    // The editor keeps its own argument text after a return.
    const extraArgs = editorDraft ? lines(editorDraft.args ?? '') : profileDraft?.extraArgs ?? []
    profileDraft = { ...(profileDraft ?? blankProfile()), harness, model: allModels[0] || '', models: allModels, extraArgs }
    editorProfile = profileDraft
  }
  function returnToStep(value: number, draft: EditorDraft) {
    profileDraft = draft.profile
    // Advanced fields and the picker describe the same selection. Reconcile
    // them before showing earlier steps so reopening cannot restore old values.
    if (harness !== profileDraft.harness) {
      editorDraft = draft
      forgetKnobs()
      draft = editorDraft
      catalogGate.invalidate()
      connectionId = ''
      harness = profileDraft.harness
      category = harness === 'opencode' ? 'opencode' : 'all'
      provider = ''; search = ''; catalog = null; preselectionMessage = ''
      if (!['custom', 'mock'].includes(harness)) loadModels()
      else catalogBusy = false
    }
    const chain = normalizeModels(profileDraft.model, profileDraft.models ?? [])
    profileDraft = { ...profileDraft, model: chain[0] ?? '', models: chain }
    fallbackModels = chain.slice(1).map(id => routeModel(catalog?.models ?? [], id) ?? id)
    model = routeModel(catalog?.models ?? [], profileDraft.model)
    const variant = model ? null : routeEffort(catalog?.models ?? [], profileDraft.model)
    if (variant) model = variant.model
    exactId = variant ? profileDraft.model : ''
    manual = !model
    manualId = profileDraft.model || ''
    editorDraft = draft
    editorProfile = null
    step = value
    focusHeading()
  }
  function back() { step = Math.max(0, step - 1); focusHeading() }
  onMount(() => {
    loadAccounts()
    if (step === 1 && !['custom', 'mock'].includes(harness)) loadModels(false, initial?.routeId)
    return () => { catalogGate.invalidate(); accountGate.invalidate() }
  })
</script>


{#if editorProfile}
  <ProfileEditor guided modelSummary={{ label: manual ? manualId.trim() || 'Harness default' : model?.label || model?.id, modelId: primaryId, harness, harnessLabel: selectedHarness.label, publisher: manual ? 'Manual entry' : model?.publisher || model?.providerId, icon: publisherIcon(model) || (iconIds.includes(selectedHarness.icon) ? selectedHarness.icon : '') }} draft={editorDraft} onback={returnToStep} original="" profile={editorProfile} harnesses={editorHarnesses} {onsaved} oncancel={(draft) => returnToStep(2, draft)} />
{:else}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="backdrop">
    <div class="wizard" role="dialog" aria-modal="true" aria-label="New profile" tabindex="-1" use:modal={{ close: oncancel, initial: () => heading }}>
      <header><strong>New profile</strong><button class="close" aria-label="Close new profile" onclick={oncancel}>×</button></header>
      <nav aria-label="Profile setup steps">
        {#each ['Harness', 'Model', 'Details', 'Profile'] as label, i}
          <button class:current={step === i} disabled={i > step} aria-current={step === i ? 'step' : undefined} onclick={() => { step = i; focusHeading() }}><span>{i < step ? '✓' : i + 1}</span>{label}</button>
          {#if i < 3}<i></i>{/if}
        {/each}
      </nav>
      <div class="body">
        <h1 tabindex="-1" bind:this={heading}>{step === 0 ? 'Choose your harness' : step === 1 ? 'Find your model' : manual ? 'Your model' : model?.label || model?.id}</h1>
        {#if step === 0}
          <div class="harness-grid">
            {#each choices as h}
              <button class="harness" class:chosen={harness === h.id} disabled={!!unready[h.id]} onclick={() => chooseHarness(h.id)}>
                <div class="h-top">{#if iconIds.includes(h.icon)}<HarnessIcon id={h.icon} />{/if}<strong>{h.label}</strong><span class="arrow">›</span></div>
                <p>{unready[h.id] || h.description}</p>
              </button>
            {/each}
          </div>
          <button class="custom" onclick={() => chooseHarness('custom')}><span class="command-icon" aria-hidden="true">&gt;_</span><div><strong>Bring your own harness</strong><p>Use a custom command and model ID.</p></div><span class="arrow">→</span></button>
          <details class="developer"><summary>Developer tools</summary><button class="text" onclick={() => chooseHarness('mock')}>Use the mock harness for testing →</button></details>
        {:else if step === 1}
          <div class="context">{#if iconIds.includes(selectedHarness.icon)}<HarnessIcon id={selectedHarness.icon} />{/if}<strong>{selectedHarness.label}</strong><button class="text push" onclick={() => { step = 0; focusHeading() }}>Change harness</button></div>
          {#if ['custom', 'mock'].includes(harness)}
            <label>Model ID<input bind:value={manualId} placeholder="Leave blank for the harness default" spellcheck="false" /></label>
            <p class="callout">{harness === 'custom' ? 'Add your command in the profile settings.' : 'The mock harness produces test output without calling a model.'}</p>
            <button class="btn fill lg" onclick={enterManually}>Model details →</button>
          {:else}
            <div class="searchbar"><input aria-label="Search models" bind:value={search} placeholder="Search models or providers…" /><select aria-label="Filter provider" bind:value={provider}><option value="">All providers</option>{#each providers as value}<option value={value}>{value}</option>{/each}</select></div>
            {#if harness === 'opencode'}
              <div class="tabs" role="group" aria-label="Model access">
                {#each ([['opencode', 'OpenCode'], ['go', 'OpenCode Go'], ['free', 'Free'], ['all', 'All models']] as const) as [value, label]}
                  <button class:active={category === value} class:secondary={value === 'all'} aria-pressed={category === value} onclick={() => { category = value; provider = '' }}>{label}<span>{models.filter(m => inCategory(m, value)).length}</span></button>
                {/each}
              </div>
            {/if}
            {#if preselectionMessage}<p class="callout" role="status">{preselectionMessage}</p>{/if}
            {#if catalogBusy}<Loading>Loading models…</Loading>
            {:else if catalogError}<div class="empty" role="alert"><p class="error">Could not load models: {catalogError}</p><button class="text" onclick={() => loadModels(true)}>Retry model discovery</button></div>
            {:else}
              <div class="catalog-note"><span>{filtered.length} {filtered.length === 1 ? 'model' : 'models'}</span><span>{catalog?.source || 'Catalog not reported'}{dateTime(catalog?.observedAt) ? ` · ${dateTime(catalog?.observedAt)}` : ''}</span><button class="text" onclick={() => loadModels(true)}>Refresh</button></div>
              {#if catalog?.message}<p class="message">{catalog.message}</p>{/if}
              <div class="results">
                {#each filtered as m (m.id)}
                  <button class="model-row" onclick={() => chooseModel(m)}>{#if publisherIcon(m)}<HarnessIcon id={publisherIcon(m)} />{/if}<div class="model-info"><strong>{m.label || m.id}</strong><small>{m.publisher || m.providerId || 'Provider not reported'}</small></div><span class="tag" class:gold={m.category === 'go'} class:sage={m.category === 'free'}>{accessLabel(m)}</span><span class="arrow">›</span></button>
                {:else}
                  <div class="empty"><h3>{models.length ? 'No matching models' : 'No model catalog reported'}</h3><p>{models.length ? 'Try another search or choose All models.' : 'Enter an exact model ID or use the harness default.'}</p>{#if search || provider}<button class="text" onclick={() => { search = ''; provider = '' }}>Clear search and provider</button>{/if}</div>
                {/each}
              </div>
            {/if}
            <div class="manual"><span>Using a model that is not listed?</span><button class="text" onclick={enterManually}>Enter a model ID or use the harness default →</button></div>
          {/if}
        {:else}
          <div class="model-context">{#if iconIds.includes(selectedHarness.icon)}<HarnessIcon id={selectedHarness.icon} />{/if}<span>{manual ? 'Manual entry' : model?.publisher || model?.providerId || 'Provider not reported'} <span class="slash">/</span> {selectedHarness.label}</span></div>
          {#if accountBusy}<Loading>Loading account status…</Loading>
          {:else if accountError}<div class="account-summary" role="alert"><div><strong class="error">Account status unavailable</strong><p>{accountError}</p></div><button class="text push" onclick={() => loadAccounts(true)}>Retry</button></div>
          {:else if account}
            <div class="account-summary"><div><strong>{account.label}</strong><p>{accountStatus(account)}</p><small>{observation(account)}</small></div>{#if onusage}<button class="text push" onclick={() => onusage?.(account.id)}>View usage →</button>{/if}<button class="text" onclick={() => loadAccounts(true)}>Refresh</button></div>
          {:else}<p class="message">{connectionId ? 'The selected account is not active for this model route.' : 'Account and plan not reported for this model route.'} <button class="text" onclick={() => loadAccounts(true)}>Refresh accounts</button></p>{/if}
          {#if connectionId}<p class="callout">This profile will use the harness’s active account.</p>{/if}
          <div class="detail-grid"><div>
            <div class="section"><h3>Model overview</h3>{#if manual || model?.description}<p>{manual ? 'Enter the exact model ID your harness expects, or leave it blank to use its default.' : model?.description}</p>{/if}{#if manual}<label>Model ID<input class="mono" bind:value={manualId} placeholder="Harness default" spellcheck="false" /></label>{:else}<span class="tag" class:gold={model?.category === 'go'} class:sage={model?.category === 'free'}>{accessLabel(model)}</span>{/if}</div>
            <div class="section fallbacks"><h3>Fallback models</h3>
              <p>If the primary model hits a usage or rate limit, Dyna automatically falls back to the next model in this list.</p>
              <ol class="chain" aria-label="Model chain">
                <li><span class="rank">#1 Primary</span><span class="mono id">{primaryId || 'Harness default'}</span></li>
                {#each fallbackModels as m, i (fallbackId(m))}
                  <li><span class="rank">#{i + 2} Fallback</span><span class="mono id" title={fallbackId(m)}>{fallbackLabel(m)}</span>
                    <span class="chain-btns"><button type="button" class="icon" aria-label={`Move ${fallbackId(m)} up`} disabled={i === 0} onclick={() => moveFallback(i, -1)}>↑</button><button type="button" class="icon" aria-label={`Move ${fallbackId(m)} down`} disabled={i === fallbackModels.length - 1} onclick={() => moveFallback(i, 1)}>↓</button><button type="button" class="icon" aria-label={`Remove ${fallbackId(m)}`} onclick={() => removeFallback(i)}>×</button></span></li>
                {/each}
              </ol>
              {#if fallbackChoices.length}
                <div class="add-row"><select aria-label="Add fallback model from catalog" bind:value={fallbackPick} onchange={addPicked}><option value="">+ Add fallback model from catalog</option>{#each fallbackChoices as m (m.id)}<option value={m.id}>{m.label || m.id}{m.category === 'go' ? ' · Go' : m.category === 'free' ? ' · Free' : ''}</option>{/each}</select></div>
              {/if}
              <div class="add-row"><input class="mono" aria-label="Fallback model ID" bind:value={fallbackManual} placeholder="Or type an exact model ID" spellcheck="false" onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addManualFallback() } }} /><button type="button" class="btn" disabled={!fallbackManual.trim()} onclick={addManualFallback}>Add</button></div>
            </div>
            <div class="section"><h3>Capabilities</h3><dl>{#each [['Context window', model?.context], ['Maximum input', model?.input], ['Maximum output', model?.output]] as [label, value]}<div><dt>{label}</dt><dd>{typeof value === 'number' && value > 0 ? value.toLocaleString() + ' tokens' : 'Not reported'}</dd></div>{/each}<div><dt>Model ID</dt><dd class="mono">{manual ? manualId.trim() || 'Harness default' : model?.id}</dd></div></dl>{#if !manual && model?.source}<p class="source">Source: {model?.source}</p>{/if}</div>
          </div><aside><h3>Account limits</h3>
            {#if account && !accountError && !accountBusy}
              {#if account.windows?.length}{#each account.windows as window (window.id)}<div class="meter"><UsageMeter {window} observedAt={account.observedAt} /></div>{/each}{:else}<p>Usage limits are not reported for this account.</p>{/if}
              <small>{observation(account)}</small>
            {:else}<p>{accountBusy ? 'Loading account status…' : 'Account usage is not available for this route.'}</p>{/if}
            {#if model?.limits?.length}<div class="published"><h3>Published limits</h3>{#each model?.limits as limit}<div class="published-limit"><strong>{limit.label}</strong><p>{limit.value}</p>{#if limit.source}<small>Source: {limit.source}</small>{/if}</div>{/each}</div>{/if}
          </aside></div>
        {/if}
      </div>
      <footer><button class="btn quiet lg" onclick={step ? back : oncancel}>{step ? '← Back' : 'Cancel'}</button>{#if step === 2}<button class="btn fill lg" onclick={settings}>Use {manual && !manualId.trim() ? 'harness default' : 'this model'} →</button>{/if}</footer>
    </div>
  </div>
{/if}

<style>
  .backdrop { position: fixed; inset: 0; z-index: 55; background: #08080aa8; display: grid; place-items: center; padding: 28px; }
  .wizard { width: min(940px, 100%); max-height: calc(100dvh - 56px); display: flex; flex-direction: column; background: var(--main); border: 1px solid var(--line); border-radius: 13px; box-shadow: 0 24px 70px #0006; min-height: min(660px, calc(100dvh - 56px)); }
  header { display: flex; align-items: center; justify-content: space-between; padding: 17px 28px; border-bottom: 1px solid var(--line); font-size: var(--t-body); }
  .close { color: var(--muted); font-size: var(--t-display); width: 32px; height: 32px; border-radius: 5px; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .close:hover { background: var(--hover); }
  }
  .close:active { background: var(--select); }
  nav { display: flex; align-items: center; gap: 14px; padding: var(--s5) 36px 0; }
  nav button { display: flex; align-items: center; gap: var(--s2); color: var(--muted); font-size: var(--t-sm); }
  nav button span { display: grid; place-items: center; border: 1px solid var(--line); border-radius: 50%; width: 23px; height: 23px; font-size: var(--t-cap); }
  nav button.current { color: var(--gold); }
  nav button.current span { border-color: var(--gold); background: var(--gold-wash); }
  nav button:disabled { color: var(--faint); cursor: default; }
  nav i { width: 34px; height: 1px; background: var(--line); }
  .body { padding: 30px 36px var(--s5); overflow-y: auto; flex: 1; min-height: 0; }
  h1 { font-size: var(--t-display); font-weight: 500; letter-spacing: -.6px; margin: 0 0 26px; overflow-wrap: anywhere; }
  h3 { font-size: var(--t-body); font-weight: 500; margin: 0 0 13px; }
  p { color: var(--muted); font-size: var(--t-ui); line-height: 1.7; margin: var(--s2) 0; }
  small { color: var(--muted); font-size: var(--t-cap); line-height: 1.6; overflow-wrap: anywhere; }
  .harness-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 13px; }
  .harness { border: 1px solid var(--line); border-radius: 9px; padding: 19px var(--s4); text-align: left; background: var(--side); transition: border-color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .harness:hover { border-color: var(--muted); background: var(--hover); }
  }
  .harness:active { background: var(--select); }
  .harness.chosen { border-color: #827255; background: var(--select); }
  .h-top { display: flex; align-items: center; gap: 10px; font-size: var(--t-body); }
  .harness p { margin: var(--s4) 0 0; font-size: var(--t-sm); }
  .arrow { color: var(--faint); margin-left: auto; font-size: var(--t-page); }
  .custom { display: flex; align-items: center; gap: 15px; width: 100%; border: 1px solid var(--line); border-radius: 8px; padding: 15px 18px; margin-top: 18px; text-align: left; }
  .custom strong { font-size: var(--t-ui); font-weight: 500; }
  .custom p { margin: 3px 0 0; font-size: var(--t-sm); }
  .command-icon { font-family: var(--mono); font-size: var(--t-page); color: var(--muted); }
  .developer { margin-top: 20px; font-size: var(--t-sm); color: var(--muted); }
  .developer button { margin-top: var(--s3); }
  .context, .account-summary { display: flex; align-items: center; gap: 14px; padding-bottom: 18px; margin-bottom: 23px; border-bottom: 1px solid var(--line); font-size: var(--t-ui); }
  .account-summary p { margin: 3px 0; font-size: var(--t-sm); }
  .push { margin-left: auto; }
  .text { color: var(--gold); font-size: var(--t-sm); text-align: left; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .text:hover { color: var(--ink); }
  }
  .searchbar { display: flex; gap: var(--s3); margin-bottom: 20px; }
  .searchbar input { flex: 1; min-width: 0; }
  .searchbar select { max-width: 210px; }
  input, select { font: inherit; font-size: var(--t-ui); color: var(--ink); background: var(--side); border: 1px solid var(--line); border-radius: 6px; padding: 11px var(--s3); }
  input:focus-visible, select:focus-visible { outline: 2px solid var(--iris-edge); }
  label { display: flex; flex-direction: column; gap: 7px; font-size: var(--t-sm); color: var(--muted); margin: 18px 0; }
  .tabs { display: flex; gap: 22px; border-bottom: 1px solid var(--line); }
  .tabs button { display: flex; align-items: center; gap: 7px; padding: 0 0 var(--s3); margin-bottom: -1px; color: var(--muted); font-size: var(--t-sm); border-bottom: 2px solid transparent; }
  .tabs button.active { color: var(--gold); border-bottom-color: var(--gold); }
  .tabs span { color: var(--faint); font-size: var(--t-cap); }
  .tabs .secondary { margin-left: auto; }
  .catalog-note { display: flex; align-items: baseline; gap: var(--s3); justify-content: space-between; color: var(--muted); font-size: var(--t-cap); padding: 15px 0 10px; }
  .catalog-note span:nth-child(2) { margin-left: auto; text-align: right; }
  .model-row { display: flex; width: 100%; align-items: center; gap: 14px; text-align: left; padding: 18px 3px; border-bottom: 1px solid var(--line); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .model-row:hover { background: var(--hover); }
  }
  .model-row:active { background: var(--select); }
  .model-info { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .model-info strong { display: block; font-size: var(--t-lead); font-weight: 500; }
  .model-info small { display: block; margin-top: var(--s1); }
  .tag { font-size: var(--t-cap); padding: var(--s1) var(--s2); border: 1px solid var(--line); border-radius: 4px; color: var(--muted); white-space: nowrap; }
  .tag.gold { color: var(--gold); background: var(--gold-wash); border-color: #66563b; }
  .tag.sage { color: var(--sage); background: #97c9950b; border-color: #435940; }
  .manual { margin-top: 23px; display: flex; gap: var(--s3); flex-wrap: wrap; font-size: var(--t-sm); color: var(--muted); }
  .empty { text-align: center; padding: var(--s6) var(--s2); }
  .empty h3 { margin-bottom: var(--s1); }
  .message, .callout { font-size: var(--t-sm); }
  .callout { border: 1px solid var(--line); border-radius: 6px; padding: var(--s3) 15px; margin: var(--s4) 0; }
  .error { color: var(--coral); }
  .model-context { display: flex; align-items: center; gap: var(--s3); color: var(--muted); font-size: var(--t-ui); margin: -12px 0 var(--s5); }
  .slash { color: var(--faint); margin: 0 6px; }
  .detail-grid { display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); gap: 42px; }
  .section + .section { border-top: 1px solid var(--line); padding-top: 22px; margin-top: 26px; }
  .section > .tag { display: inline-block; margin-top: 10px; }
  dl { margin: 0; font-size: var(--t-sm); }
  dl > div { display: flex; justify-content: space-between; gap: 18px; padding: 10px 0; border-bottom: 1px solid var(--line-soft); }
  dt { color: var(--muted); flex: none; } dd { margin: 0; text-align: right; overflow-wrap: anywhere; min-width: 0; }
  .source { font-size: var(--t-cap); overflow-wrap: anywhere; }
  aside { align-self: start; padding: 22px; background: var(--side); border: 1px solid var(--line); border-radius: 9px; }
  .meter { padding: 15px 0; border-bottom: 1px solid var(--line); }
  .published { padding-top: 22px; margin-top: 22px; border-top: 1px solid var(--line); }
  .published-limit { margin-top: var(--s4); font-size: var(--t-sm); }
  .published-limit strong { font-weight: 500; } .published-limit p { font-size: var(--t-sm); margin: var(--s1) 0; }
  .chain { list-style: none; margin: var(--s3) 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .chain li { display: flex; align-items: center; gap: 10px; padding: var(--s2) 10px; background: var(--side); border: 1px solid var(--line); border-radius: 6px; font-size: var(--t-sm); }
  .rank { color: var(--gold); white-space: nowrap; flex: none; min-width: 84px; }
  .chain .id { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .chain-btns { display: flex; gap: 2px; flex: none; }
  .icon { width: 26px; height: 26px; border-radius: 5px; color: var(--muted); font-size: var(--t-lead); }
  .icon:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .icon:hover:not(:disabled) { background: var(--hover); color: var(--ink); }
  }
  .icon:active:not(:disabled) { background: var(--select); }
  .icon:disabled { opacity: .35; cursor: default; }
  .add-row { display: flex; gap: var(--s2); margin-top: var(--s2); }
  .add-row select, .add-row input { flex: 1; min-width: 0; }
  footer { display: flex; justify-content: space-between; gap: var(--s3); padding: 17px 28px; border-top: 1px solid var(--line); }
  @media (max-width: 720px) { .harness-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .detail-grid { gap: var(--s5); grid-template-columns: 1fr; } .body { padding: 25px 22px; } nav { padding: 20px 22px 0; gap: var(--s2); } nav i { width: 15px; } .account-summary { flex-wrap: wrap; } }
  @media (max-width: 480px) { .backdrop { padding: 0; } .wizard { border-radius: 0; border: 0; width: 100%; height: 100dvh; max-height: 100dvh; } .harness-grid { grid-template-columns: 1fr; } nav { gap: 6px; } nav button { font-size: var(--t-cap); gap: var(--s1); } nav i { flex: 1; width: 0; } .searchbar { flex-direction: column; } .searchbar select { max-width: none; } .tabs { gap: var(--s3); flex-wrap: wrap; } .tabs button { font-size: var(--t-cap); } .tag { white-space: normal; } .catalog-note { flex-wrap: wrap; } }
</style>
