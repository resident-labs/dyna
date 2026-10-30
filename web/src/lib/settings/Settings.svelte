<script lang="ts">
  // The owner's configuration in the shared settings modal. Everything reads
  // and writes through configsvc, the same code `dyna profiles` and
  // `dyna settings` use.
  import { untrack } from 'svelte'
  import SettingsShell from '../ui/SettingsShell.svelte'
  import ProfilesPane from './ProfilesPane.svelte'
  import HarnessesPane from './harnesses/HarnessesPane.svelte'
  import SkillsPane from './skills/SkillsPane.svelte'
  import RulesPane from './RulesPane.svelte'
  import McpPane from './mcp/McpPane.svelte'
  import AccountsPane from './accounts/AccountsPane.svelte'
  import SettingsPane from './SettingsPane.svelte'
  import DevicesPane from './DevicesPane.svelte'
  import Services from '../services/Services.svelte'
  import type { ModelSelection } from './profiles/state.ts'

  interface Props {
    start?: string
    pairNow?: boolean
    roomId?: string
    accountId?: string
    // rulesVersion changes when a rule may have changed; the Rules pane reloads.
    rulesVersion?: number
    profile?: string
    initialSelection?: ModelSelection | null
    oncreateprofile?: (selection: ModelSelection) => void
    // repo is the open room's repository, which the Services pane uses.
    repo?: string
    onclose: () => void
  }
  let { start = 'profiles', pairNow = false, roomId = '', repo = '', accountId = '', rulesVersion = 0, profile = '', initialSelection = null, oncreateprofile = () => {}, onclose }: Props = $props()

  let pane = $state(untrack(() => start)) // the pane to open first
  let accountToOpen = $state(untrack(() => accountId))
  let profileToOpen = $state(untrack(() => profile))
  let profileSelection = $state<ModelSelection | null>(untrack(() => initialSelection))
  let busy = $state(false) // a modal inside a pane owns Esc while open

  const nav = [
    { id: 'profiles', label: 'Profiles', hint: 'Who does the work' },
    { id: 'harnesses', label: 'Harnesses', hint: 'Install and maintain coding agents' },
    { id: 'skills', label: 'Skills', hint: 'Skills every session can read' },
    { id: 'rules', label: 'Rules', hint: 'Standing rules for every room' },
    { id: 'mcp', label: 'MCP', hint: 'MCP servers Dyna manages' },
    { id: 'accounts', label: 'Accounts', hint: 'Sign-ins, usage and active account' },
    { id: 'services', label: 'Services', hint: 'What Dyna runs by itself, and hooks' },
    { id: 'settings', label: 'Settings', hint: 'How Dyna behaves' },
    { id: 'devices', label: 'Devices', hint: 'Phones and browsers' },
  ]

  function selectPane(next: string) {
    if (next !== pane) { profileSelection = null; profileToOpen = '' }
    pane = next
  }
  function viewAccount(id: string) { profileSelection = null; profileToOpen = ''; accountToOpen = id; pane = 'accounts' }
  function viewProfile(name: string) { profileSelection = null; profileToOpen = name; pane = 'profiles' }
</script>

{#snippet foot()}{#if pane === 'settings'}Display preferences apply at once in this browser. Other changes save{:else}Changes save{/if} at once and apply to new sessions and runs. The <span class="mono">dyna</span> CLI sees the same values.{/snippet}

<SettingsShell label="Settings" title="Configuration" {nav} {pane} onselect={selectPane} {busy} wide {onclose} foot={['harnesses', 'accounts'].includes(pane) ? null : foot}>
  {#if pane === 'profiles'}
    <ProfilesPane initialSelection={profileSelection} initialProfile={profileToOpen} {roomId} onusage={viewAccount} onmodal={(open) => (busy = open)} />
  {:else if pane === 'harnesses'}
    <HarnessesPane onmodal={(open) => (busy = open)} />
  {:else if pane === 'skills'}
    <SkillsPane />
  {:else if pane === 'rules'}
    <RulesPane {roomId} version={rulesVersion} onmodal={(open) => (busy = open)} />
  {:else if pane === 'mcp'}
    <McpPane />
  {:else if pane === 'accounts'}
    <AccountsPane accountId={accountToOpen} {roomId} {oncreateprofile} onprofile={viewProfile} onmodal={(open) => (busy = open)} />
  {:else if pane === 'settings'}
    <SettingsPane {roomId} />
  {:else if pane === 'services'}
    <Services scope="global" {roomId} {repo} />
  {:else if pane === 'devices'}
    <DevicesPane {pairNow} onmodal={(open) => (busy = open)} />
  {/if}
</SettingsShell>
