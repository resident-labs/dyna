<script lang="ts">
  // Hooks: the commands the owner added. A repository hook runs for every room
  // on that repository; a hook on this computer runs for all of them. A feed
  // card's "View log" lands here with its run open, so each request scrolls
  // the section into view and remounts the repository list on that run.
  import { tick } from 'svelte'
  import Section from '../ui/Section.svelte'
  import HookList from './HookList.svelte'
  import type { HooksFocus } from '../app/settingsPanel.svelte.ts'

  let { repo = '', focus = null }: { repo?: string; focus?: HooksFocus | null } = $props()

  $effect(() => {
    if (!focus?.request) return
    tick().then(() => document.getElementById('hooks')?.scrollIntoView({ block: 'start' }))
  })
</script>

<Section id="hooks" title="Hooks" description="Commands you add. Each runs on an event or a schedule.">
  {#if repo}
    {#key focus?.request}<HookList title="This repository" {repo} scope="repository" focus={focus?.run ?? null} />{/key}
  {/if}
  <HookList title="This computer" scope="global" />
</Section>
