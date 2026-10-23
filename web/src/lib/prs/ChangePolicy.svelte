<script lang="ts">
  // The rules of one change: whether it uses the room rules, any override
  // with its recorded reason, and why review was skipped. The review
  // settings drawer shows it.
  //
  // Props:
  //   change    a Changes row (policy, policyOverride, skipReasons)
  //   room      the room's policy, to find what differs from it
  //   roomId    the room, for the host calls
  //   onchange  reload Changes after a save
  import { api } from '../../api.ts'
  import { POLICY_REVIEW, normalizePolicy, policyLabel, checksText, overrideKeys, buildOverride, saveOverride, resetOverride, type NormalizedPolicy, type OverrideField, type OverrideForm } from './pr.ts'
  import type { ChangeDetail, ChangePolicy } from '../api/change.ts'
  import { errorText } from '../errorText.ts'

  interface Props {
    change: Partial<ChangeDetail>
    room?: ChangePolicy | null
    roomId?: string
    onchange?: () => void | Promise<void>
  }
  let { change, room: roomProp = null, roomId = '', onchange = () => {} }: Props = $props()

  // The room policy is the base an override differs from. Load it unless the
  // parent passed it.
  let loaded = $state<ChangePolicy | null>(null)
  let room = $derived(roomProp ?? loaded)
  $effect(() => {
    if (roomProp || !roomId) return
    api.roomChangePolicy(roomId).then((p: ChangePolicy) => (loaded = p), () => {})
  })

  let policy = $derived(normalizePolicy(change.policy ?? room))
  let ov = $derived(change.policyOverride ?? null)
  let keys = $derived(overrideKeys(ov))
  let open = $state(false)
  let form = $state<OverrideForm | null>(null)
  let reason = $state('')
  let busy = $state('')
  let error = $state('')

  const FIELDS: { key: 'review'; label: string; opts: { id: string; label: string }[] }[] = [
    { key: 'review', label: 'Review', opts: POLICY_REVIEW },
  ]
  // The settings an override changes, in the order the form lists them.
  let shownFields = $derived([...FIELDS.map((f): { key: OverrideField; label: string } => ({ key: f.key, label: f.label })), { key: 'requiredChecks' as const, label: 'Required checks' }].filter((f) => keys.includes(f.key)))
  const show = (k: OverrideField, p: NormalizedPolicy) => (k === 'requiredChecks' ? checksText(p.requiredChecks) || 'none' : policyLabel[k](p[k]))

  function edit() {
    form = { requiredChecks: checksText(policy.requiredChecks), review: policy.review }
    reason = ov?.reason ?? ''
    error = ''
    open = true
  }

  // Compare against the room policy, or against the change's own settings
  // when the host sent no room policy.
  let draft = $derived(form ? buildOverride(room ?? change.policy, form, reason) : null)
  let changed = $derived(draft ? overrideKeys(draft).length > 0 : false)

  async function save(run: () => Promise<unknown>) {
    if (busy) return
    busy = 'save'
    error = ''
    try {
      await run()
      open = false
      await onchange()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = ''
    }
  }
  const submit = () => {
    const edited = form
    if (edited) return save(() => saveOverride(api, roomId, change.changeId ?? '', room ?? change.policy, edited, reason))
  }
  const reset = () => save(() => resetOverride(api, roomId, change.changeId ?? ''))
</script>

<h4>This PR{#if keys.length}<span>own rules</span>{/if}</h4>
{#if keys.length}
  <dl class="pol">
    {#each shownFields as { key, label }}
      <div><dt>{label}</dt><dd>{show(key, policy)}</dd></div>
    {/each}
  </dl>
{:else}
  <p class="why">Uses the room rules.</p>
{/if}
{#if keys.length && ov?.reason}<p class="why"><strong>Reason</strong> {ov.reason}</p>{/if}
{#each change.skipReasons ?? [] as s}<p class="why"><strong>Skipped</strong> {s}</p>{/each}

{#if open && form}
  <div class="form" aria-label="Override policy">
    <div class="grid">
      {#each FIELDS as f}
        <label for="ov-{f.key}">{f.label}</label>
        <select id="ov-{f.key}" bind:value={form[f.key]} disabled={!!busy}>{#each f.opts as o}<option value={o.id}>{o.label}</option>{/each}</select>
      {/each}
      <label for="ov-req">Required checks</label>
      <input id="ov-req" type="text" bind:value={form.requiredChecks} disabled={!!busy} placeholder="Names, separated by commas" />
    </div>
    <label for="ov-reason">Reason{changed ? ' (required)' : ''}</label>
    <textarea id="ov-reason" rows="2" bind:value={reason} placeholder="Why this change differs from the room policy" disabled={!!busy}></textarea>
    {#if error}<p class="err">{error}</p>{/if}
    <div class="fa">
      <button class="send" onclick={submit} disabled={!!busy}>{busy === 'save' ? 'Saving…' : changed ? 'Save override' : keys.length ? 'Remove override' : 'Save'}</button>
      <button class="mini" onclick={() => (open = false)} disabled={!!busy}>Cancel</button>
    </div>
  </div>
{:else}
  <div class="fa">
    <button class="mini" onclick={edit}>{keys.length ? "Edit this PR's rules" : "Change rules for this PR"}</button>
    {#if keys.length}<button class="mini" onclick={reset} disabled={!!busy}>{busy === 'save' ? 'Resetting…' : 'Reset to room policy'}</button>{/if}
  </div>
  {#if error}<p class="err">{error}</p>{/if}
{/if}

<style>
  h4 { display: flex; gap: 8px; align-items: center; margin: 12px 0 6px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
  h4 span { font-weight: 500; letter-spacing: 0; color: var(--gold); text-transform: none; }
  .pol { margin: 0 0 8px; display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 6px 12px; background: var(--main); border-radius: 8px; padding: 10px 12px; }
  .pol dt { font-size: 12px; font-weight: 600; color: var(--muted); }
  .pol dd { margin: 2px 0 0; font-size: 13.5px; color: var(--ink-2); overflow-wrap: anywhere; }
  .why { margin: 0 0 6px; font-size: 13.5px; color: var(--ink-2); overflow-wrap: anywhere; }
  .why strong { color: var(--muted); font-size: 12px; margin-right: 4px; }
  .form { display: flex; flex-direction: column; gap: 6px; margin-bottom: 8px; }
  .grid { display: grid; grid-template-columns: max-content 1fr; gap: 6px 10px; align-items: center; }
  label { font-size: 12px; font-weight: 600; color: var(--muted); }
  select, input, textarea { background: var(--rail); border: 1px solid var(--line); border-radius: 6px; color: var(--ink); font: inherit; font-size: 13.5px; padding: 4px 8px; min-width: 0; }
  textarea { width: 100%; resize: vertical; padding: 6px 8px; }
  input:focus, textarea:focus { border-color: var(--iris-edge); outline: none; }
  .fa { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-bottom: 6px; }
  .err { margin: 0; color: var(--coral); font-size: 12.5px; }
  .mini { font-size: 12.5px; font-weight: 600; color: var(--ink-2); border-radius: 6px; padding: 4px 10px; background: var(--raise); }
  .mini:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .send { background: var(--gold); color: #18161d; font-size: 13px; font-weight: 600; border-radius: 6px; padding: 5px 12px; }
  button:disabled { opacity: 0.5; cursor: default; }
  @media (max-width: 760px) {
    .mini, .send { min-height: 44px; padding: 0 14px; }
    select, input { font-size: 16px; min-height: 44px; }
    textarea { font-size: 16px; }
  }
</style>
