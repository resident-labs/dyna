<script lang="ts">
  import type { Snippet } from "svelte";
  import HostsPane from "../src/lib/ui/HostsPane.svelte";
  let selected = $state<string | undefined>("ovh-rise5");
</script>
{#snippet bar(title: string, count: number | undefined, action: Snippet | undefined)}
  <header><h1>{title}</h1><span>{count}</span>{#if action}{@render action()}{/if}</header>
{/snippet}
<main><nav>Settings <span>Hosts</span></nav><HostsPane {bar} phone={false} navigate={() => {}} signedout={() => {}} {selected} onselect={name => selected = name} onclose={() => selected = undefined} /></main>
<style>
 main { height: 100vh; display: flex; flex-direction: column; }
 nav { padding: 20px 28px; border-bottom: 1px solid var(--line); color: var(--muted); }
 nav span { margin-left: 30px; color: var(--text); }
 header { display: flex; align-items: center; gap: 12px; padding: 20px 28px; }
 h1 { font-size: 20px; margin: 0; }
</style>
