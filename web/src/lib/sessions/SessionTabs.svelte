<script lang="ts">
  // Messages, Media, Computer, and Shell. Review lives beside chat.
  import Tabs from '../ui/Tabs.svelte'
  import { sessionTabs } from './sessionTabs.ts'
  // computer: the session's computer, or null when computers are off.
  // shells: the session's running shells; the tab shows while above zero.
  import type { SessionTabsInput } from './sessionTabs.ts'
  let { tab = $bindable('chat'), media = [], computer = null, shells = 0 }: SessionTabsInput = $props()
  let tabs = $derived(sessionTabs({ media, computer, shells, tab }))
</script>

<Tabs {tabs} bind:tab label="Session views" />
