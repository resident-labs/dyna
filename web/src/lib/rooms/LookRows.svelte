<script lang="ts">
  // The feed choices for every feed view. Settings is the one place that sets
  // them, so each row writes the same shared value.
  import Switch from '../ui/Switch.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import { CHOICES, setting } from './look.ts'
  import { look, choose } from './look.svelte.ts'
</script>

<div class="rows">
  {#each CHOICES as c (c.id)}
    <div class="row">
      <div class="text">
        <span class="title">{c.label}</span>
        <span class="hint">{c.hint}</span>
      </div>
      {#if c.options}
        <Segmented options={c.options} value={setting(look, c.id)} label={c.label} onchange={(v: string | boolean) => choose(c.id, v)} />
      {:else}
        <Switch on={setting(look, c.id) === true} label={c.label} onchange={(v: boolean) => choose(c.id, v)} />
      {/if}
    </div>
  {/each}
</div>

<style>
  /* Narrow: the control drops under its label. */
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2) var(--s4); padding: 10px var(--s3); }
  .row + .row { border-top: 1px solid var(--line-soft); }
  .text { flex: 1 1 200px; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .title { font-size: var(--t-body); font-weight: 600; color: var(--ink); }
  .hint { font-size: var(--t-sm); color: var(--muted); }
</style>
