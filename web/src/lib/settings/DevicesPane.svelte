<script lang="ts">
  // Settings > Devices: browsers paired with a code, and Resident Labs
  // Connect sign-ins, each with Revoke. "Pair a device" makes a code and a
  // QR. A device or session is as strong as a shell on this computer, so
  // revoking is one click plus a confirm.
  import { errorText } from '../errorText.ts'
  import { onMount, untrack } from 'svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import InlineConfirm from '../ui/InlineConfirm.svelte'
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../ui/motion.js'
  import TabHeader from '../rooms/TabHeader.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { api } from '../../api.js'
  import { ago } from '../ui/format.js'
  import Confirm from '../ui/Confirm.svelte'
  import PairDevice from './PairDevice.svelte'
  import type { Device } from '../api/settings.ts'

  // A revoke the owner is asked about: all of a kind, or one row inline.
  interface Revoke {
    kind: string
    id: string
    key?: string
    title?: string
    lines?: string[]
    action?: string
    busy?: boolean
    error?: string
  }
  // One row's scopes being edited.
  interface ScopeEdit {
    token: number
    key: string
    id: string
    scopes: Record<string, boolean>
    busy: boolean
    error: string
  }
  interface Detail {
    text: string
    mono?: boolean
    warn?: boolean
  }

  let { pairNow = false, onmodal = () => {} }: { pairNow?: boolean; onmodal?: (open: boolean) => void } = $props()

  let list = $state<Device[] | null>(null)
  let error = $state('')
  let pairing = $state(untrack(() => pairNow)) // opened from "Pair a device" elsewhere
  let confirm = $state<Revoke | null>(null) // revoke all: {kind, id, title, lines, action, busy, error}
  let revoking = $state<Revoke | null>(null) // one row, asked inline: {key, kind, id, busy, error}
  let editing = $state<ScopeEdit | null>(null) // one row: {token, key, id, scopes, busy, error}
  let editToken = 0

  const scopeNames = ['read', 'operate', 'files', 'terminal', 'computers', 'admin']

  $effect(() => { onmodal(pairing || !!confirm); return () => onmodal(false) })

  let paired = $derived((list ?? []).filter((d) => d.kind === 'paired'))
  let sessions = $derived((list ?? []).filter((d) => d.kind === 'connect'))

  async function load() {
    try {
      list = (await api.devices()) ?? []
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  onMount(load)

  function until(iso?: string) {
    const ms = Date.parse(iso ?? '') - Date.now()
    if (!iso || Number.isNaN(ms)) return ''
    if (ms <= 0) return 'expired'
    const m = Math.round(ms / 60000)
    if (m < 60) return `expires in ${m} min`
    const h = Math.round(m / 60)
    if (h < 48) return `expires in ${h} h`
    return `expires in ${Math.round(h / 24)} days`
  }

  function host(origin?: string) {
    try {
      return new URL(origin ?? '').host
    } catch {
      return origin || ''
    }
  }

  // The quiet line under a device name: browser, address, last use, expiry.
  function details(d: Device) {
    const out: Detail[] = []
    if (d.device) out.push({ text: d.device })
    if (d.origin) out.push({ text: host(d.origin), mono: true })
    out.push({ text: `seen ${d.lastSeen && !d.lastSeen.startsWith('0001') ? ago(d.lastSeen) : 'never'}` })
    const u = until(d.expires)
    if (u) out.push({ text: u, warn: u === 'expired' })
    if (d.scopeLabel) out.unshift({ text: d.scopeLabel, warn: !!d.legacy })
    return out
  }

  // A small picture of the device from its name.
  function kindOf(d: Pick<Device, 'name' | 'device'>) {
    const n = `${d.name ?? ''} ${d.device ?? ''}`.toLowerCase()
    if (/iphone|android phone|pixel|phone|mobile/.test(n)) return 'phone'
    if (/ipad|tablet/.test(n)) return 'tablet'
    return 'laptop'
  }

  function askAll(kind: string) {
    const n = kind === 'connect' ? sessions.length : paired.length
    confirm = {
      kind,
      id: 'all',
      title: kind === 'connect' ? `End all ${n} Connect sessions?` : `Revoke all ${n} paired devices?`,
      lines: ['Each one loses access now. This includes the device you are using, if it is in the list.'],
      action: 'Revoke all',
    }
  }

  function startScopes(d: Device) {
    const have = new Set(d.legacy ? scopeNames : (d.scopes ?? []))
    const scopes: Record<string, boolean> = {}
    for (const name of scopeNames) scopes[name] = have.has(name)
    editToken += 1
    editing = { token: editToken, key: d.kind + d.id, id: d.id, scopes, busy: false, error: '' }
    revoking = null
  }

  async function saveScopes() {
    const current = editing
    if (!current) return
    const names = scopeNames.filter((name) => current.scopes[name])
    if (names.length === 0) {
      editing = { ...current, error: 'Choose at least one scope.' }
      return
    }
    const token = current.token
    editing = { ...current, busy: true, error: '' }
    try {
      await api.setDeviceScopes(current.id, names)
      if (editing?.token === token) editing = null
      load()
    } catch (e) {
      if (editing?.token === token) editing = { ...current, busy: false, error: errorText(e) }
    }
  }

  function toggleScope(name: string, event: Event & { currentTarget: HTMLInputElement }) {
    if (!editing || editing.busy) return
    editing = { ...editing, scopes: { ...editing.scopes, [name]: event.currentTarget.checked } }
  }

  async function revoke(target: Revoke, set: (value: Revoke | null) => void) {
    set({ ...target, busy: true, error: '' })
    try {
      await api.revokeDevice(target.kind, target.id)
      set(null)
      load()
    } catch (e) {
      set({ ...target, busy: false, error: errorText(e) })
    }
  }
</script>

{#snippet glyph(kind: string)}
  <span class="dev" aria-hidden="true">
    {#if kind === 'phone'}
      <svg viewBox="0 0 24 24" width="20" height="20"><rect x="7" y="3" width="10" height="18" rx="2.2" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M11 18h2" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
    {:else if kind === 'tablet'}
      <svg viewBox="0 0 24 24" width="20" height="20"><rect x="5" y="3" width="14" height="18" rx="2.2" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M11 18h2" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
    {:else}
      <svg viewBox="0 0 24 24" width="20" height="20"><rect x="4" y="5" width="16" height="11" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M2.5 19h19" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
    {/if}
  </span>
{/snippet}

{#snippet rows(items: Device[], kind: string)}
  <div class="table">
    {#each items as d (d.kind + d.id)}
      <div class="item" transition:slide={{ duration: motion(BASE) }}>
        {@render glyph(kindOf(d))}
        <span class="main">
          <span class="name">{d.name || 'Unnamed device'}{#if d.current}<span class="here">this device</span>{/if}</span>
          <span class="sub">
            {#each details(d) as part, i}{#if i}<span class="dot" aria-hidden="true">·</span>{/if}<span class:mono={part.mono} class:warn={part.warn}>{part.text}</span>{/each}
          </span>
          {#if revoking?.key === d.kind + d.id && revoking.error}<span class="err" role="alert">{revoking.error}</span>{/if}
          {#if editing?.key === d.kind + d.id && editing.error}<span class="err" role="alert">{editing.error}</span>{/if}
        </span>
        {#if editing?.key === d.kind + d.id}
          <span class="scopes">
            {#each scopeNames as name}
              <label><input type="checkbox" checked={editing.scopes[name]} disabled={editing.busy} onchange={(event) => toggleScope(name, event)} /> {name}</label>
            {/each}
            <button class="btn quiet" type="button" disabled={editing.busy} onclick={saveScopes}>Save</button>
            <button class="btn quiet" type="button" disabled={editing.busy} onclick={() => (editing = null)}>Cancel</button>
          </span>
        {:else if revoking?.key === d.kind + d.id}
          {@const asked = revoking}
          <InlineConfirm ask={d.current ? 'Revoke? You will be signed out.' : 'Revoke?'} action="Revoke" busy={revoking.busy} onconfirm={() => revoke(asked, (v) => (revoking = v))} oncancel={() => (revoking = null)} />
        {:else}
          <button class="btn quiet" type="button" onclick={() => startScopes(d)} aria-label="Change scopes for {d.name || 'device'}">Scopes</button>
          <button class="btn quiet" type="button" onclick={() => { editing = null; revoking = { key: d.kind + d.id, kind: d.kind, id: d.id } }} aria-label="Revoke {d.name || 'device'}">Revoke</button>
        {/if}
      </div>
    {/each}
  </div>
  {#if items.length > 1}
    <button class="all" onclick={() => askAll(kind)}>{kind === 'connect' ? 'End all Connect sessions' : 'Revoke all paired devices'}</button>
  {/if}
{/snippet}

<section>
  <TabHeader flush title="Devices" blurb="Phones and browsers that can use this Dyna. The line under each name is what that device is allowed to do.">
    {#snippet action()}
      <button class="btn fill" onclick={() => (pairing = true)}>
        <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M10 4v12M4 10h12" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
        Pair a device
      </button>
    {/snippet}
  </TabHeader>

  {#if error}
    <p class="err">{error}</p>
  {:else if !list}
    <Skeleton label="Loading devices" rows={3} />
  {:else}
    <h3 class="section-label">Paired with a code<span>{paired.length}</span></h3>
    {#if paired.length}
      {@render rows(paired, 'paired')}
    {:else}
      <EmptyState title="No paired devices">Pair one to use Dyna from a phone on your network or tunnel.</EmptyState>
    {/if}

    <h3 class="section-label">Resident Labs Connect<span>{sessions.length}</span></h3>
    {#if sessions.length}
      {@render rows(sessions, 'connect')}
    {:else}
      <EmptyState title="No Connect sessions">A browser that signs in through Connect shows here.</EmptyState>
    {/if}
  {/if}
</section>

{#if pairing}
  <PairDevice onclose={() => { pairing = false; load() }} />
{/if}
{#if confirm}
  {@const asked = confirm}
  <Confirm title={asked.title ?? ''} lines={asked.lines} action={asked.action} busy={asked.busy} error={asked.error} onconfirm={() => revoke(asked, (v) => (confirm = v))} oncancel={() => (confirm = null)} />
{/if}

<style>
  .section-label span { margin-left: var(--s2); letter-spacing: 0; }
  .table { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; overflow: hidden; }
  .item { display: flex; align-items: center; gap: 14px; padding: var(--s3) 14px; }
  .item + .item { border-top: 1px solid var(--line-soft); }
  .dev { width: 36px; height: 36px; border-radius: 9px; background: var(--raise); color: var(--muted); display: grid; place-items: center; flex: none; }
  .main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .name { font-weight: 600; font-size: var(--t-body); color: var(--ink); display: flex; align-items: center; gap: var(--s2); }
  .here { font-size: var(--t-cap); font-weight: 600; color: var(--sage); background: rgba(151, 201, 149, 0.12); border-radius: 4px; padding: 0 6px; }
  .sub { font-size: var(--t-sm); color: var(--faint); overflow-wrap: anywhere; }
  .sub .mono { font-size: var(--t-sm); }
  .warn { color: var(--coral); }
  .dot { margin: 0 6px; }
  .all { margin: var(--s2) 2px 0; font-size: var(--t-sm); color: var(--faint); text-decoration: underline; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .all:hover { color: var(--coral); }
  }
  .scopes { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; font-size: var(--t-sm); color: var(--muted); }
  .scopes label { display: inline-flex; align-items: center; gap: 4px; }
  .err { color: var(--coral); }
  @media (max-width: 760px) {
    .btn { min-height: 44px; }
    .item { flex-wrap: wrap; }
    .main { min-width: 160px; }
    .all { min-height: 44px; }
  }
</style>
