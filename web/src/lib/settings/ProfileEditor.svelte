<script lang="ts">
  import { errorText } from '../errorText.ts'
  import { dialog as modal } from '../ui/dialog.ts'
  // Create or edit one profile. Env values are write-only: the editor shows
  // which keys exist, can replace or remove a value, and never receives one.
  import { api } from '../../api.ts'
  import HarnessIcon from './harnesses/HarnessIcon.svelte'
  import { normalizeModels, profileEffort, chooseEffort, compactBlock, compactFor, compactWindows, compactHint, type EditableProfile, type EditorDraft, type ModelSummary } from './profiles/state.ts'
  import { requestGate } from './usage/state.ts'
  import { stateLabel, observedRows, driftText, probeRows, runProbe, healthChip, type ProbeResult } from './support.ts'
  import type { Catalog } from '../api/account.ts'
  import type { ProfileCheck, ProfileView } from '../api/profile.ts'

  interface Props {
    original?: string
    profile: EditableProfile
    harnesses?: string[]
    profiles?: ProfileView[]
    onsaved: (profile: ProfileView) => void
    oncancel: (draft: EditorDraft) => void
    guided?: boolean
    onasksupport?: (profile: EditableProfile) => void
    onchecked?: (check: ProfileCheck) => void
    modelSummary?: ModelSummary | null
    onback?: (step: number, draft: EditorDraft) => void
    draft?: EditorDraft | null
  }
  let { original = '', profile, harnesses = [], profiles = [], onsaved, oncancel, guided = false, onasksupport, onchecked, modelSummary = null, onback, draft = null }: Props = $props()

  let p = $state<EditableProfile>(profile)
  // Fallbacks follow the primary model (p.model). The saved chain is [p.model, ...fallbacks].
  let fallbacks = $state(normalizeModels(profile.model ?? '', draft?.models ?? profile.models ?? []).slice(1))
  let newFallback = $state('')
  let args = $state(draft?.args ?? (profile.extraArgs ?? []).join('\n'))
  let command = $state(draft?.command ?? (profile.command ?? []).join('\n'))
  let envSet = $state<Record<string, string>>(draft?.envSet ?? {}) // key -> new value
  let envRemove = $state<string[]>(draft?.envRemove ?? [])
  let newKey = $state(draft?.newKey ?? '')
  let newVal = $state(draft?.newVal ?? '')
  let replacing = $state(draft?.replacing ?? '') // the env key whose value is being replaced
  let replacementValue = $state(draft?.replacementValue ?? '')
  let advancedOpen = $state(draft?.advancedOpen ?? false)
  let busy = $state(false)
  let error = $state('')
  let first = $state<HTMLInputElement>()

  // focus is a Svelte action: the replace field takes focus when it opens.
  const focus = (node: HTMLElement) => node.focus()
  const lines = (s: string) => s.split('\n').map((x) => x.trim()).filter(Boolean)
  let keys = $derived([...new Set([...(profile.envKeys ?? []), ...Object.keys(envSet)])].sort())

  const chain = () => normalizeModels(p.model ?? '', [p.model ?? '', ...fallbacks])
  // A profile fallback continues the session on another registered profile.
  const otherProfiles = $derived((profiles ?? []).map((x) => x.name).filter((n) => n && n !== (p.name ?? '')))

  // Reasoning effort and auto-compact come from the harness catalog. The new
  // profile wizard and the edit view both set them here.
  let catalog = $state<Catalog | null>(null)
  const catalogGate = requestGate()
  $effect(() => {
    const harness = p.harness
    const ticket = catalogGate.next()
    catalog = null
    if (['custom', 'mock'].includes(harness)) return
    api.profileModels(harness, false).then((c: Catalog) => { if (catalogGate.current(ticket)) catalog = c }, () => {})
  })
  const knob = $derived(profileEffort(catalog?.models ?? [], p.model ?? '', lines(args)))
  const defaultEffort = $derived(knob.model?.efforts?.find((e) => e.default))
  const rule = $derived(catalog?.compact)
  // Each model of the chain, fallbacks included, compacts against its own window.
  const windows = $derived(compactWindows(catalog?.models ?? [], chain(), p.compact))
  const compactOff = $derived(rule ? compactBlock(rule, windows) : '')
  const compactMax = $derived(rule?.window ? Math.min(...Object.values(windows)) - 1 : rule?.max)
  // The stored windows follow the chain, and a harness without a threshold clears the field.
  $effect(() => {
    if (!rule || !p.compact) return
    const next = compactFor(rule, windows, p.compact.tokens)
    if (JSON.stringify(next) !== JSON.stringify($state.snapshot(p.compact))) p.compact = next
  })
  function pickEffort(id: string) {
    if (!knob.model) return
    const next = chooseEffort(knob.model, id, p.model ?? '', lines(args))
    p.model = next.model
    args = next.args.join('\n')
  }
  function addFallback() {
    const id = newFallback.trim()
    if (!id) return
    fallbacks = normalizeModels('', [...fallbacks, id]).filter((m) => m !== (p.model ?? '').trim())
    newFallback = ''
  }
  function moveFallback(i: number, by: number) {
    const j = i + by
    const a = fallbacks[i], b = fallbacks[j]
    if (a === undefined || b === undefined) return
    const next = [...fallbacks]
    next[i] = b
    next[j] = a
    fallbacks = next
  }
  function promoteFallback(i: number) {
    const promoted = fallbacks[i]
    if (promoted === undefined) return
    const old = (p.model ?? '').trim()
    const next = fallbacks.filter((_, k) => k !== i)
    p.model = promoted
    fallbacks = old ? [old, ...next] : next
  }
  function removeFallback(i: number) { fallbacks = fallbacks.filter((_, k) => k !== i) }

  function stageEnv() {
    const k = newKey.trim()
    if (!k) return
    envSet = { ...envSet, [k]: newVal }
    envRemove = envRemove.filter((x) => x !== k)
    newKey = ''
    newVal = ''
  }

  function dropEnv(k: string) {
    const { [k]: _, ...rest } = envSet
    envSet = rest
    if ((profile.envKeys ?? []).includes(k)) envRemove = [...new Set([...envRemove, k])]
  }

  // Keep raw text and write-only values in memory while the wizard changes steps.
  function leave(step: number | null = null) {
    if (busy) return
    const value = $state.snapshot({ profile: { ...p, models: chain() }, models: chain(), args, command, envSet, envRemove, newKey, newVal, replacing, replacementValue, advancedOpen })
    if (step === null) oncancel(value)
    else onback?.(step, value)
  }

  // Test this profile: one live probe, shown as progress, then a result per
  // capability. The probe runs in Go; the editor only waits, never blocks.
  let support = $state(profile.support)
  let view = $derived({ ...profile, support })
  let test = $state<ProbeResult<ProfileCheck> | { phase: 'running' } | null>(null) // {phase: 'running' | 'done' | 'failed' | 'timeout', check?, error?}
  let elapsed = $state(0)
  async function runTest() {
    if (test?.phase === 'running') return
    test = { phase: 'running' }
    elapsed = 0
    const tick = setInterval(() => (elapsed += 1), 1000)
    const r = await runProbe<ProfileCheck>(() => api.configCheckProfile(original, true))
    clearInterval(tick)
    test = r
    if (r.phase === 'done' && r.check?.support) {
      support = r.check.support
      onchecked?.(r.check)
    }
  }
  let chip = $derived(healthChip(view))
  let rows = $derived(test?.phase === 'done' ? probeRows(test.check) : [])
  let probeError = $derived(test?.phase === 'done' ? test.check?.probeError : '')

  async function save() {
    if (busy || (guided && (!p.name?.trim() || (p.harness === 'custom' && !command.trim())))) return
    busy = true
    error = ''
    try {
      const v = await api.saveProfile({
        original,
        profile: { ...$state.snapshot(p), model: chain()[0] ?? '', models: chain(), extraArgs: lines(args), command: lines(command) },
        envSet: $state.snapshot(envSet),
        envRemove: $state.snapshot(envRemove),
      })
      onsaved(v)
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 's' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      save()
    }
  }
</script>


{#snippet fallbackEditor()}
      <h3>Fallback models <span>used in order if the primary model hits a usage or rate limit</span></h3>
      {#if fallbacks.length}
        <ol class="chain" aria-label="Model chain">
          <li><span class="rank">#1 Primary</span><span class="mono id">{p.model || 'Harness default'}</span></li>
          {#each fallbacks as m, i (m)}
            <li><span class="rank">#{i + 2} Fallback</span><span class="mono id">{m}</span>
              <span class="chain-btns"><button type="button" aria-label={`Move ${m} up`} title="Move up" disabled={i === 0} onclick={() => moveFallback(i, -1)}>↑</button><button type="button" aria-label={`Move ${m} down`} title="Move down" disabled={i === fallbacks.length - 1} onclick={() => moveFallback(i, 1)}>↓</button><button type="button" aria-label={`Promote ${m} to primary`} title="Promote to primary" onclick={() => promoteFallback(i)}>★</button><button type="button" class="bad" aria-label={`Remove ${m}`} title="Remove" onclick={() => removeFallback(i)}>×</button></span></li>
          {/each}
        </ol>
      {/if}
      <div class="envrow add"><input class="mono" bind:value={newFallback} placeholder="Fallback model ID" aria-label="Fallback model ID" spellcheck="false" onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addFallback() } }} /><button type="button" onclick={addFallback} disabled={!newFallback.trim()}>Add fallback</button></div>
{/snippet}

{#snippet profileFallbackEditor()}
      <h3>Fallback profile <span>continues this session when its harness hits a usage limit and no account is left</span></h3>
      <label>Next profile
        <select bind:value={p.fallback}>
          <option value="">None, stay on this profile</option>
          {#if p.fallback && !otherProfiles.includes(p.fallback)}<option value={p.fallback}>{p.fallback} (not registered)</option>{/if}
          {#each otherProfiles as name (name)}<option value={name}>{name}</option>{/each}
        </select>
        <span class="field-hint">The next profile's own fallback continues the chain, for example glm, then deepseek, then sonnet. Dyna never moves a session back automatically.</span>
      </label>
{/snippet}

{#snippet modelKnobs()}
      {#if catalog}
        <div class="grid knobs">
          {#if knob.model?.efforts?.length}
            <label>Reasoning effort<select value={knob.effort} onchange={(e) => pickEffort(e.currentTarget.value)}><option value="">Harness default{defaultEffort ? ` (${defaultEffort.label || defaultEffort.id})` : ''}</option>{#each knob.model.efforts as e (e.id)}<option value={e.id}>{e.label || e.id}</option>{/each}</select></label>
          {/if}
          <label>Compact at (tokens)<input type="number" min={rule?.min || 1} max={compactMax || undefined} value={p.compact?.tokens ?? ''} placeholder="Harness default" disabled={!!compactOff} oninput={(e) => (p.compact = compactFor(rule, windows, Math.round(+e.currentTarget.value)))} />
            <span class="field-hint">{compactHint(rule, compactOff, compactMax)}</span></label>
        </div>
      {/if}
{/snippet}

{#snippet advancedFields()}
      <h3>Limits <span>0 means no limit</span></h3>
      <div class="grid three">
        <label>Timeout (seconds)<input type="number" min="0" bind:value={p.timeoutSec} /></label>
        <label>At once per run<input type="number" min="0" bind:value={p.maxConcurrent} /></label>
        <label>Calls per run<input type="number" min="0" bind:value={p.maxCallsPerRun} /></label>
      </div>

      {#if p.harness === 'custom' && !guided}
        <h3>Command <span>one argument per line; {'{{prompt}}'} and {'{{model}}'} are filled in</span></h3>
        <textarea bind:value={command} rows="3" class="mono" spellcheck="false"></textarea>
      {/if}
      <h3>Extra arguments <span>one per line, passed to the harness</span></h3>
      <textarea bind:value={args} rows="2" class="mono" spellcheck="false" placeholder="--flag=value"></textarea>

      <h3>Environment <span>values are write-only and never shown</span></h3>
      <div class="env">
        {#each keys as k (k)}
          {@const removed = envRemove.includes(k)}
          <div class="envrow" class:removed>
            <span class="mono k">{k}</span>
            {#if replacing === k}
              <input type="password" class="mono" placeholder="new value, then Enter" bind:value={replacementValue} autocomplete="off" use:focus onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); envSet = { ...envSet, [k]: e.currentTarget.value }; replacing = ''; replacementValue = '' } }} />
            {:else}
              <span class="mono val">{envSet[k] !== undefined ? 'new value set' : removed ? 'will be removed' : '••••••••'}</span>
            {/if}
            {#if removed}
              <button type="button" onclick={() => (envRemove = envRemove.filter((x) => x !== k))}>Keep</button>
            {:else}
              <button type="button" onclick={() => { replacing = replacing === k ? '' : k; replacementValue = '' }}>{replacing === k ? 'Cancel' : 'Replace'}</button>
              <button type="button" class="bad" onclick={() => dropEnv(k)}>Remove</button>
            {/if}
          </div>
        {/each}
        <div class="envrow add">
          <input class="mono k" bind:value={newKey} placeholder="KEY" spellcheck="false" />
          <input class="mono" type="password" bind:value={newVal} placeholder="value" autocomplete="off" onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); stageEnv() } }} />
          <button type="button" onclick={stageEnv} disabled={!newKey.trim()}>Add</button>
        </div>
      </div>
{/snippet}


<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <form use:modal={{ close: leave, busy, initial: () => first, preventScroll: guided }} onkeydown={keydown} class="card" class:guided role="dialog" aria-modal="true" aria-label={original ? `Edit ${original}` : 'New profile'} tabindex="-1" onclick={(e) => e.stopPropagation()} onsubmit={(e) => { e.preventDefault(); save() }}>
    <header>
      {#if guided}<strong>New profile</strong><button type="button" class="close" aria-label="Back to model details" onclick={() => leave()} disabled={busy}>×</button>{:else}<h2>{original ? `Edit ${original}` : 'New profile'}</h2>
      <p>Saved to your profiles file, the same one <span class="mono">dyna profiles</span> edits.</p>{/if}
    </header>
    {#if guided}
      <nav aria-label="Profile setup steps">{#each ['Harness', 'Model', 'Details', 'Profile'] as label, i}<button type="button" class:current={i === 3} disabled={busy || i === 3} aria-current={i === 3 ? 'step' : undefined} onclick={() => i < 3 && leave(i)}><span>{i < 3 ? '✓' : '4'}</span>{label}</button>{#if i < 3}<i></i>{/if}{/each}</nav>
    {/if}
    <div class="body">
      {#if guided}
        <h1>Make it your own</h1>
        <div class="settings-grid"><div>
          <div class="fields">
            <label>Profile name<input bind:this={first} bind:value={p.name} class="mono" placeholder="e.g. quick-research" spellcheck="false" /></label>
            <label>What will you use it for?<textarea bind:value={p.description} rows="3" placeholder="e.g. Fast research and small code changes"></textarea></label>
            {@render modelKnobs()}
            {#if p.harness === 'custom'}<label>Command<textarea bind:value={command} rows="3" class="mono" spellcheck="false" placeholder="One argument per line"></textarea><span class="field-hint">Use {'{{prompt}}'} and {'{{model}}'}.</span></label>{/if}
          </div>
          <div class="score-section"><h3>Workflow scores</h3><p class="explain">Your ratings help workflows choose a profile. Higher is better.</p>
          <div class="scores">{#each ([['taste', 'Taste'], ['diagnostics', 'Diagnostics'], ['intelligence', 'Intelligence'], ['cost', 'Cost efficiency']] as const) as [k, label]}<label class="score"><span>{label}</span><input type="range" min="1" max="10" bind:value={p[k]} /><span class="v">{p[k]}</span></label>{/each}</div></div>
          <details class="advanced" bind:open={advancedOpen}><summary>Advanced settings</summary>
            <div class="grid"><label>Harness<select bind:value={p.harness}>{#each harnesses as h}<option value={h}>{h}</option>{/each}</select></label><label>Model<input bind:value={p.model} class="mono" placeholder="the harness default" spellcheck="false" /></label></div>
            {@render fallbackEditor()}
            <h3>Behavior</h3><div class="toggles">{#each ([['default', 'Default profile', 'Used when a session or agent names none'], ['disabled', 'Disabled', 'Hidden from / and from workflows']] as const) as [k, label, hint]}<label class="toggle"><input type="checkbox" bind:checked={p[k]} /><span class="box" aria-hidden="true"></span><span class="t"><strong>{label}</strong><em>{hint}</em></span></label>{/each}</div>
            {@render advancedFields()}
          </details>
        </div><aside class="model-summary">
          <div class="summary-title">{#if modelSummary?.icon && p.harness === modelSummary.harness}<HarnessIcon id={modelSummary.icon} />{/if}<div><strong>{p.model === modelSummary?.modelId && p.harness === modelSummary?.harness ? modelSummary.label : p.model || 'Harness default'}</strong><p>{p.harness === modelSummary?.harness ? modelSummary.harnessLabel : p.harness}{p.harness === modelSummary?.harness && p.model === modelSummary?.modelId && modelSummary.publisher ? ` · ${modelSummary.publisher}` : ''}</p></div></div>
          <div class="summary-section"><h3>Profile behavior</h3>{#each ([['safeMode', 'Safe mode', 'Keep the harness’s permission prompts.']] as const) as [k, label, hint]}<label class="toggle"><input type="checkbox" bind:checked={p[k]} /><span class="box" aria-hidden="true"></span><span class="t"><strong>{label}</strong><em>{hint}</em></span></label>{/each}</div>
          <div class="summary-section"><p>This profile will use:</p>{#if chain().length > 1}<ol class="chain" aria-label="Model chain">{#each chain() as m, i (m)}<li><span class="rank">#{i + 1} {i ? 'fallback' : 'primary'}</span><span class="mono">{m}</span></li>{/each}</ol>{:else}<div class="mono">{p.model || 'Harness default'}</div>{/if}</div>
        </aside></div>
      {:else}
      <div class="grid">
        <label>Name<input bind:this={first} bind:value={p.name} class="mono" placeholder="opus" spellcheck="false" /></label>
        <label>Harness
          <select bind:value={p.harness}>
            {#each harnesses as h}<option value={h}>{h}</option>{/each}
          </select>
        </label>
        <label class="wide">Model<input bind:value={p.model} class="mono" placeholder="the harness default" spellcheck="false" /></label>
        <div class="wide">{@render fallbackEditor()}</div>
        <div class="wide">{@render profileFallbackEditor()}</div>
        <div class="wide">{@render modelKnobs()}</div>
        <label class="wide">Description<input bind:value={p.description} placeholder="What it is good at" /></label>
      </div>

      <h3>Scores <span>1 to 10, higher is better; workflows read them</span></h3>
      <div class="scores">
        {#each ([['taste', 'Taste'], ['diagnostics', 'Diagnostics'], ['intelligence', 'Intelligence'], ['cost', 'Cost efficiency']] as const) as [k, label]}
          <label class="score">
            <span>{label}</span>
            <input type="range" min="1" max="10" bind:value={p[k]} />
            <span class="v">{p[k]}</span>
          </label>
        {/each}
      </div>

      <h3>Behavior</h3>
      <div class="toggles">
        {#each ([['default', 'Default profile', 'Used when a session or agent names none'], ['safeMode', 'Safe mode', 'Keep the harness’s own permission prompts'], ['disabled', 'Disabled', 'Hidden from / and from workflows']] as const) as [k, label, hint]}
          <label class="toggle">
            <input type="checkbox" bind:checked={p[k]} />
            <span class="box" aria-hidden="true"></span>
            <span class="t"><strong>{label}</strong><em>{hint}</em></span>
          </label>
        {/each}
      </div>

      {#if original && support?.items?.length}
        <h3>Harness support <span>what Dyna reads from {profile.harness} output</span></h3>
        <ul class="support" aria-label="Harness support">
          {#each support.items as it (it.key)}
            <li class={it.state}>
              <span class="mark" aria-hidden="true">{it.state === 'yes' ? '✓' : it.state === 'no' ? '×' : '–'}</span>
              <span class="what"><strong>{it.label}</strong>{#if it.reason}<em>{it.reason}</em>{/if}</span>
              <span class="state">{stateLabel(it.state)}</span>
            </li>
          {/each}
        </ul>
        <dl class="health" aria-label="Harness health">
          <div><dt>Status</dt><dd>{#if chip}<span class="chip {chip.tone}" title={chip.title}>{chip.text}</span>{:else}Working{/if}</dd></div>
          <div><dt>{support.binary ? support.binary.split('/').pop() : 'CLI'} version</dt><dd>{support.version || 'Unknown'}</dd></div>
          <div><dt>Version change</dt><dd>{driftText(view)}</dd></div>
          {#each observedRows(view) as [label, value]}<div><dt>{label}</dt><dd>{value}</dd></div>{/each}
        </dl>
        <div class="actions">
          {#if support.partial && onasksupport}
            <button type="button" class="btn ask" onclick={() => onasksupport(profile)} disabled={busy}>Ask an agent to add support</button>
          {/if}
          <button type="button" class="btn ask" onclick={runTest} disabled={busy || test?.phase === 'running' || profile.disabled}>{test?.phase === 'running' ? 'Testing…' : 'Test this profile'}</button>
        </div>
        <div class="probe" aria-live="polite">
          {#if test?.phase === 'running'}
            <p class="running"><span class="dot" aria-hidden="true"></span>Running a short real turn on the saved profile… {elapsed} s. This uses the model and can take up to 2 minutes.</p>
          {:else if test?.phase === 'timeout'}
            <p class="bad">The test took too long. It may still finish. Open this profile again later to see the result.</p>
          {:else if test?.phase === 'failed'}
            <p class="bad" role="alert">The test could not run: {test.error}</p>
          {:else if test?.phase === 'done'}
            <p class={probeError ? 'bad' : 'good'}>{probeError ? `The test did not pass: ${probeError}` : 'The test passed.'}</p>
            {#if rows.length}
              <ul class="support" aria-label="Test result">
                {#each rows as r (r.key)}
                  <li class={r.state === 'na' ? 'unavailable' : r.state}>
                    <span class="mark" aria-hidden="true">{r.state === 'yes' ? '✓' : r.state === 'no' ? '×' : '–'}</span>
                    <span class="what"><strong>{r.label}</strong><em>{r.words}</em></span>
                  </li>
                {/each}
              </ul>
            {/if}
          {/if}
        </div>
      {/if}

      {@render advancedFields()}
      {/if}
    </div>
    <footer>
      {#if error}<p class="err" role="alert">{error}</p>{:else}<p class="hint"><kbd>Ctrl</kbd>+<kbd>S</kbd> saves · <kbd>Esc</kbd> {guided ? 'returns to details' : 'cancels'}</p>{/if}
      <button type="button" class="btn quiet lg" onclick={() => leave()} disabled={busy}>{guided ? '← Details' : 'Cancel'}</button>
      <button type="submit" class="btn fill lg" disabled={busy || !p.name?.trim() || (guided && p.harness === 'custom' && !command.trim())}>{busy ? 'Saving…' : original ? 'Save changes' : 'Create profile'}</button>
    </footer>
  </form>
</div>

<style>
  /* One track the window's size: an auto track grows to fit wide content. */
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; grid-template: minmax(0, 1fr) / minmax(0, 1fr); place-items: center; z-index: 55; padding: var(--s6); }
  .card { width: min(640px, 100%); max-height: min(860px, calc(100vh - 64px)); display: flex; flex-direction: column; background: var(--side); border-radius: 12px; box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); outline: none; }
  header { padding: 20px 22px 0; }
  h2 { margin: 0; font-size: var(--t-head); }
  header p { margin: var(--s1) 0 0; color: var(--faint); font-size: var(--t-ui); }
  .body { padding: 6px 22px var(--s2); overflow-y: auto; min-height: 0; }
  select { appearance: none; background-image: linear-gradient(45deg, transparent 50%, var(--muted) 50%), linear-gradient(135deg, var(--muted) 50%, transparent 50%); background-position: calc(100% - 16px) 50%, calc(100% - 11px) 50%; background-size: 5px 5px; background-repeat: no-repeat; }
  h3 { font-size: var(--t-sm); font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 20px 0 var(--s2); }
  h3 span { text-transform: none; letter-spacing: 0; font-weight: 400; color: var(--faint); margin-left: 6px; }
  .grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: var(--s3); margin-top: 14px; }
  .grid.three { grid-template-columns: repeat(3, minmax(0, 1fr)); margin-top: 0; }
  .grid input, .grid select { width: 100%; }
  .grid.knobs { margin-top: 0; }
  .wide { grid-column: 1 / -1; }
  label { display: flex; flex-direction: column; gap: 6px; font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  input, select, textarea { min-width: 0; box-sizing: border-box; background: var(--rail); border: 1px solid transparent; border-radius: 6px; color: var(--ink); padding: var(--s2) 10px; font: inherit; font-size: var(--t-body); font-weight: 400; outline: none; }
  input.mono, textarea.mono { font-family: var(--mono); font-size: var(--t-ui); }
  input:focus, select:focus, textarea:focus { border-color: var(--iris-edge); }
  textarea { width: 100%; resize: vertical; box-sizing: border-box; }
  .scores { display: flex; flex-direction: column; gap: var(--s2); }
  .score { flex-direction: row; align-items: center; gap: var(--s3); font-weight: 500; color: var(--ink-2); font-size: var(--t-body); }
  .score > span:first-child { width: 120px; }
  .score input { flex: 1; padding: 0; accent-color: var(--iris); background: none; }
  .score .v { width: 24px; text-align: right; font-variant-numeric: tabular-nums; color: var(--ink); font-weight: 600; }
  .toggles { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: var(--s2); }
  .toggle { flex-direction: row; align-items: flex-start; gap: 10px; background: var(--rail); border-radius: 8px; padding: 10px var(--s3); cursor: pointer; font-weight: 400; }
  .toggle input { position: absolute; opacity: 0; pointer-events: none; }
  .box { width: 18px; height: 18px; border-radius: 5px; border: 2px solid #5a565c; flex: none; margin-top: 1px; }
  .toggle input:checked + .box { background: var(--iris); border-color: var(--iris); box-shadow: inset 0 0 0 3px var(--rail); }
  .toggle input:focus-visible + .box { outline: 2px solid var(--iris-edge); outline-offset: 2px; }
  .t { display: flex; flex-direction: column; gap: 2px; }
  .t strong { font-size: var(--t-body); color: var(--ink); font-weight: 600; }
  .t em { font-style: normal; font-size: var(--t-sm); color: var(--faint); }
  .chain { list-style: none; margin: 0 0 var(--s2); padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .chain li { display: flex; align-items: center; gap: 10px; background: var(--rail); border-radius: 6px; padding: 6px var(--s2); font-size: var(--t-ui); }
  .chain .rank { flex: none; min-width: 84px; font-size: var(--t-sm); color: var(--gold); }
  .chain .id { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .chain-btns { display: flex; gap: 2px; flex: none; }
  .chain-btns button { width: 26px; height: 26px; border-radius: 5px; color: var(--muted); }
  .chain-btns button:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .chain-btns button:hover:not(:disabled) { background: var(--hover); color: var(--ink); }
  }
  .chain-btns button:active:not(:disabled) { background: var(--select); }
  .chain-btns button.bad { color: var(--coral); }
  .chain-btns button:disabled { opacity: 0.35; cursor: default; }
  .env { display: flex; flex-direction: column; gap: 6px; }
  .support { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .support li { display: flex; align-items: flex-start; gap: 10px; background: var(--rail); border-radius: 8px; padding: 9px var(--s3); }
  .support .mark { width: 16px; text-align: center; font-weight: 700; color: var(--sage); flex: none; }
  .support li.no .mark { color: var(--coral); }
  .support li.unavailable .mark { color: var(--gold); }
  .support .what { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .support strong { font-size: var(--t-body); color: var(--ink); font-weight: 600; }
  .support em { font-style: normal; font-size: var(--t-sm); color: var(--faint); }
  .support .state { font-size: var(--t-sm); color: var(--muted); flex: none; }
  .health { margin: 10px 0 0; display: flex; flex-direction: column; gap: var(--s1); }
  .health div { display: flex; justify-content: space-between; gap: var(--s3); font-size: var(--t-ui); }
  .health dt { color: var(--faint); }
  .health dd { margin: 0; color: var(--ink-2); text-align: right; }
  .chip { font-size: var(--t-cap); font-weight: 600; border-radius: 4px; padding: 1px 6px; }
  .chip.warn { color: var(--gold); background: var(--gold-wash); }
  .chip.bad { color: var(--coral); background: rgba(239, 138, 128, 0.1); }
  .actions { display: flex; flex-wrap: wrap; gap: var(--s2); margin-top: 10px; }
  .actions .ask { margin-top: 0; }
  .probe p { margin: 10px 0 0; font-size: var(--t-ui); color: var(--ink-2); }
  .probe .support { margin-top: var(--s2); }
  .probe .good { color: var(--sage); }
  .probe .bad { color: var(--coral); }
  .probe .running { display: flex; align-items: center; gap: var(--s2); color: var(--gold); }
  .probe .dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; animation: pulse 1.2s ease-in-out infinite; flex: none; }
  @keyframes pulse { 50% { opacity: 0.3; } }
  .ask { margin-top: 10px; }
  .envrow { display: flex; align-items: center; gap: var(--s2); background: var(--rail); border-radius: 6px; padding: 6px var(--s2); }
  .envrow.removed .k { text-decoration: line-through; color: var(--faint); }
  .envrow .k { min-width: 170px; margin-right: var(--s3); flex: none; font-size: var(--t-ui); color: var(--ink-2); }
  .envrow.add .k { padding: 6px var(--s2); background: var(--main); }
  .envrow.add input:not(.k) { flex: 1; background: var(--main); padding: 6px var(--s2); }
  .envrow input[type='password'] { flex: 1; padding: 6px var(--s2); background: var(--main); }
  .val { flex: 1; color: var(--faint); font-size: var(--t-ui); }
  .envrow button { font-size: var(--t-sm); color: var(--muted); padding: var(--s1) var(--s2); border-radius: 5px; }
  .envrow button:not(:disabled) { transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .envrow button:hover:not(:disabled) { background: var(--hover); color: var(--ink); }
  }
  .envrow button:active:not(:disabled) { background: var(--select); }
  .envrow button.bad { color: var(--coral); }
  .envrow button:disabled { opacity: 0.4; cursor: default; }
  footer { display: flex; align-items: center; gap: var(--s2); padding: 14px 22px 18px; border-top: 1px solid var(--line-soft); }
  footer p { flex: 1; margin: 0; font-size: var(--t-sm); }
  .hint { color: var(--faint); }
  .err { color: var(--coral); }
  kbd { font-family: var(--sans); font-weight: 600; color: var(--muted); font-size: var(--t-cap); }

  .guided { width: min(940px, 100%); background: var(--main); border: 1px solid var(--line); max-height: calc(100dvh - 56px); min-height: min(660px, calc(100dvh - 56px)); }
  .guided header { display: flex; align-items: center; justify-content: space-between; padding: 17px 28px; border-bottom: 1px solid var(--line); font-size: var(--t-body); }
  .close { width: 32px; height: 32px; color: var(--muted); font-size: var(--t-display); }
  nav { display: flex; align-items: center; gap: 14px; padding: var(--s5) 36px 0; }
  nav button { display: flex; align-items: center; gap: var(--s2); color: var(--muted); font-size: var(--t-sm); }
  nav button span { display: grid; place-items: center; border: 1px solid var(--line); border-radius: 50%; width: 23px; height: 23px; font-size: var(--t-cap); }
  nav button.current { color: var(--gold); }
  nav button.current span { border-color: var(--gold); background: var(--gold-wash); }
  nav i { width: 34px; height: 1px; background: var(--line); }
  .guided .body { padding: 30px 36px var(--s5); flex: 1; }
  .guided header, .guided nav, .guided footer { flex-shrink: 0; }
  h1 { font-size: var(--t-display); font-weight: 500; letter-spacing: -.6px; margin: 0 0 26px; }
  .settings-grid { display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); gap: 42px; }
  .field-hint { font-size: var(--t-cap); font-weight: 400; color: var(--muted); }
  .fields { display: flex; flex-direction: column; gap: 20px; }
  .guided label { font-weight: 400; font-size: var(--t-sm); }
  .guided input:not([type=range]), .guided textarea, .guided select { padding: 11px var(--s3); border-color: var(--line); background: var(--side); font-size: var(--t-ui); }
  .guided h3 { text-transform: none; letter-spacing: 0; font-size: var(--t-body); font-weight: 500; color: var(--ink); margin-top: 0; margin-bottom: 13px; }
  .score-section, .summary-section { margin-top: 25px; padding-top: 22px; border-top: 1px solid var(--line); }
  .guided .score-section h3 { margin-bottom: 7px; }
  .explain { font-size: var(--t-sm); color: var(--muted); margin: 0 0 18px; }
  .guided .score { font-size: var(--t-ui); }
  .guided .score input { accent-color: var(--gold); }
  .advanced { border-top: 1px solid var(--line); margin-top: 26px; padding-top: 19px; }
  .advanced summary { color: var(--muted); font-size: var(--t-ui); cursor: pointer; margin-bottom: 18px; }
  .advanced h3 { margin-top: var(--s5); }
  .guided .toggles { grid-template-columns: 1fr; }
  .model-summary { align-self: start; padding: 22px; background: var(--side); border: 1px solid var(--line); border-radius: 9px; min-width: 0; }
  .summary-title { display: flex; align-items: center; gap: var(--s3); }
  .summary-title strong { font-size: var(--t-body); font-weight: 500; overflow-wrap: anywhere; }
  .model-summary p { color: var(--muted); font-size: var(--t-sm); margin: 5px 0 0; }
  .model-summary .mono { overflow-wrap: anywhere; margin-top: 10px; font-size: var(--t-cap); }
  .guided .toggle { padding: 9px 0; background: none; }
  .guided .t strong { font-size: var(--t-ui); font-weight: 400; }
  .guided .t em { color: var(--muted); font-size: var(--t-cap); }
  .guided .toggle input:checked + .box { background: var(--gold); border-color: var(--gold); }
  .guided footer { padding: 17px 28px; flex-wrap: wrap; }
  .guided footer .err { flex: 0 0 100%; overflow-wrap: anywhere; }
  .guided .envrow { flex-wrap: wrap; }
  .guided .envrow .k { min-width: 0; overflow-wrap: anywhere; }
  @media (max-width: 720px) { .settings-grid { grid-template-columns: 1fr; gap: var(--s5); } .guided .body { padding: 25px 22px; } nav { padding: 20px 22px 0; gap: var(--s2); } nav i { width: 15px; } }
  @media (max-width: 480px) { .guided { border: 0; max-height: 100dvh; } nav { gap: 6px; } nav button { font-size: var(--t-cap); gap: var(--s1); } nav i { flex: 1; width: 0; } .guided footer .hint { display: none; } .guided footer .btn.quiet { margin-right: auto; } }

  /* A phone: the editor fills the screen and every grid stacks. */
  @media (max-width: 760px) {
    .backdrop { padding: 0; }
    .card { width: 100%; max-height: none; height: 100dvh; border-radius: 0; padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom); }
    .body { padding: 6px var(--s4) var(--s2); }
    .grid, .grid.three, .toggles { grid-template-columns: minmax(0, 1fr); }
    .score > span:first-child { width: 96px; }
    .envrow .k { min-width: 0; flex: 1; }
  }
</style>
