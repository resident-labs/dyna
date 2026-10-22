<script lang="ts">
  // Messages, Media, Computer, Shell, Changes, and Pull requests.
  import Tabs from '../ui/Tabs.svelte'
  import { sessionTabs } from './sessionTabs.ts'
  // prs: the session's tracked pull requests; the tab shows when it has any.
  // computer: the session's computer, or null when computers are off.
  // shells: the session's running shells; the tab shows while above zero.
  // changes: files changed in the session's worktree; the tab shows when it is above zero.
  import type { SessionTabsInput } from './sessionTabs.ts'
  let { tab = $bindable('chat'), media = [], prs = [], computer = null, changes = 0, shells = 0 }: SessionTabsInput = $props()
  let tabs = $derived(sessionTabs({ media, prs, computer, changes, shells, tab }))
</script>

<Tabs {tabs} bind:tab label="Session views" />
