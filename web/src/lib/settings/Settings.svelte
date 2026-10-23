<script lang="ts">
  // The owner's configuration in the shared settings modal. Everything reads
  // and writes through configsvc, the same code `dyna profiles` and
  // `dyna settings` use.
  import { untrack } from 'svelte'
  import SettingsShell from '../ui/SettingsShell.svelte'
  import TabHeader from '../rooms/TabHeader.svelte'
  import ProfilesPane from './ProfilesPane.svelte'
  import HarnessesPane from './harnesses/HarnessesPane.svelte'
  import UsagePane from './usage/UsagePane.svelte'
  import SettingsPane from './SettingsPane.svelte'
  import DevicesPane from './DevicesPane.svelte'
  import type { ModelSelection } from './profiles/state.ts'
  import type { UpdateNotice } from '../api/settings.ts'

  interface Props {
    update?: UpdateNotice | null
    dataDir?: string
    start?: string
    pairNow?: boolean
    roomId?: string
    accountId?: string
    profile?: string
    initialSelection?: ModelSelection | null
    oncreateprofile?: (selection: ModelSelection) => void
    onclose: () => void
  }
  let { update = null, dataDir = '', start = 'profiles', pairNow = false, roomId = '', accountId = '', profile = '', initialSelection = null, oncreateprofile = () => {}, onclose }: Props = $props()

  let pane = $state(untrack(() => start)) // the pane to open first
  let usageAccountId = $state(untrack(() => accountId))
  let profileToOpen = $state(untrack(() => profile))
  let profileSelection = $state<ModelSelection | null>(untrack(() => initialSelection))
  let busy = $state(false) // a modal inside a pane owns Esc while open

  const nav = [
    { id: 'profiles', label: 'Profiles', hint: 'Who does the work' },
    { id: 'harnesses', label: 'Harnesses', hint: 'Install and maintain coding agents' },
    { id: 'usage', label: 'Usage', hint: 'Accounts and activity' },
    { id: 'settings', label: 'Settings', hint: 'How Dyna behaves' },
    { id: 'devices', label: 'Devices', hint: 'Phones and browsers' },
    { id: 'about', label: 'Updates', hint: 'Version and data' },
  ]

  function selectPane(next: string) {
    if (next !== pane) { profileSelection = null; profileToOpen = '' }
    pane = next
  }
  function viewUsage(id: string) { profileSelection = null; profileToOpen = ''; usageAccountId = id; pane = 'usage' }
  function viewProfile(name: string) { profileSelection = null; profileToOpen = name; pane = 'profiles' }
</script>

{#snippet foot()}{#if pane === 'settings'}Display preferences apply at once in this browser. Other changes save{:else}Changes save{/if} at once and apply to new sessions and runs. The <span class="mono">dyna</span> CLI sees the same values.{/snippet}

<SettingsShell label="Settings" title="Configuration" nav={nav.map((n) => (n.id === 'about' && update?.text ? { ...n, dot: 'Update available' } : n))} {pane} onselect={selectPane} {busy} wide={pane === 'harnesses'} {onclose} foot={['harnesses', 'usage'].includes(pane) ? null : foot}>
  {#if pane === 'profiles'}
    <ProfilesPane initialSelection={profileSelection} initialProfile={profileToOpen} {roomId} onusage={viewUsage} onmodal={(open) => (busy = open)} />
  {:else if pane === 'harnesses'}
    <HarnessesPane onmodal={(open) => (busy = open)} />
  {:else if pane === 'usage'}
    <UsagePane accountId={usageAccountId} {roomId} {oncreateprofile} onprofile={viewProfile} onharnesses={() => selectPane('harnesses')} onmodal={(open) => (busy = open)} />
  {:else if pane === 'settings'}
    <SettingsPane />
  {:else if pane === 'devices'}
    <DevicesPane {pairNow} onmodal={(open) => (busy = open)} />
  {:else}
    <section>
      <TabHeader flush title="Updates" blurb="Dyna checks for a new release at most once a day. It never installs one by itself, because running sessions use the installed binary." />
      {#if update?.text}
        <div class="card hot">
          <strong>Dyna {update.latest} is available</strong>
          <p>You have {update.current}. Run <span class="mono">dyna update</span> in a terminal to install it, then restart this app.</p>
        </div>
      {:else}
        <div class="card">
          <strong>You are up to date</strong>
          <p>Or the daily check is off. Turn it on in Settings with "Check for updates".</p>
        </div>
      {/if}
      <h3 class="section-label">Data</h3>
      <p class="mono path">{dataDir}</p>
    </section>
  {/if}
</SettingsShell>

<style>
  .card { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; padding: var(--s4) 18px; }
  .card.hot { border-color: color-mix(in srgb, var(--gold) 40%, transparent); background: var(--gold-wash); }
  .card strong { font-size: var(--t-lead); }
  .card p { margin: 6px 0 0; color: var(--muted); font-size: var(--t-body); }
  .path { color: var(--ink-2); font-size: var(--t-ui); overflow-wrap: anywhere; }
</style>
