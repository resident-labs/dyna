<script>
  // The feed choices. With a channel scope each row sets that channel's own
  // value and a dot marks a row that differs from Settings; without one each
  // row sets the default for every channel.
  import Switch from '../ui/Switch.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import { CHOICES, setting, differs } from './look.ts'
  import { look, choose } from './look.svelte.js'

  let { scope = null } = $props()
  let changed = $derived(scope === null ? [] : differs(look, scope))
</script>

<div class="rows" class:menu={scope !== null}>
  {#each CHOICES as c (c.id)}
    <div class="row">
      <div class="text">
        <span class="title">{c.label}{#if changed.includes(c.id)}<span class="dot" title="Differs from Settings"></span>{/if}</span>
        {#if scope === null}<span class="hint">{c.hint}</span>{/if}
      </div>
      {#if c.options}
        <Segmented options={c.options} value={setting(look, scope, c.id)} label={c.label} onchange={(v) => choose(scope, c.id, v)} />
      {:else}
        <Switch on={setting(look, scope, c.id)} label={c.label} onchange={(v) => choose(scope, c.id, v)} />
      {/if}
    </div>
  {/each}
  {#if scope !== null}
    <div class="foot">
      <span>{changed.length ? `${changed.length} ${changed.length === 1 ? 'choice differs' : 'choices differ'} from Settings` : 'Follows Settings'}</span>
      {#if changed.length}<button onclick={() => CHOICES.forEach((c) => choose(scope, c.id, null))}>Reset</button>{/if}
    </div>
  {/if}
</div>

<style>
  /* Narrow: the control drops under its label. */
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2) var(--s4); padding: 10px var(--s3); }
  .row + .row { border-top: 1px solid var(--line-soft); }
  .text { flex: 1 1 200px; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .title { font-size: var(--t-body); font-weight: 600; color: var(--ink); }
  .hint { font-size: var(--t-sm); color: var(--muted); }
  .dot { display: inline-block; width: 6px; height: 6px; margin-left: var(--s2); vertical-align: middle; border-radius: 50%; background: var(--iris); }
  .foot { display: flex; align-items: center; justify-content: space-between; gap: var(--s3); padding: var(--s2) var(--s3) var(--s1); border-top: 1px solid var(--line-soft); font-size: var(--t-sm); color: var(--faint); }
  .foot button { color: var(--iris); font-size: var(--t-sm); font-weight: 600; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .foot button:hover { color: var(--iris-hover); }
  }
</style>
