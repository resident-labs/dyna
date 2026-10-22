<script>
  import { ago } from '../ui/format.js'
  import { areas, byArea, location, historyLabel } from './issues.js'

  // The Issues panel. It sits beside Pinned and uses the same card shape.
  // open and history are Issue lists from roomsvc; the buttons call back with
  // the issue and App runs the call.
  let { open = [], history = [], onhistory, onclose, oncloseissue, ongoal, onexport } = $props()

  let tab = $state('open')
  let area = $state('')
  let list = $derived(tab === 'open' ? byArea(open, area) : history)
  let chips = $derived(areas(open))
  const age = (ms) => ago(new Date(ms).toISOString())
  function show(t) { tab = t; if (t === 'history') onhistory() }
</script>

<aside class="issues" aria-label="Issues">
  <header>
    <h2>Issues <span class="n">{open.length}</span></h2>
    <button class="x" onclick={onclose} title="Close (Esc)" aria-label="Close issues">✕</button>
  </header>
  <div class="tabs" role="tablist">
    <button role="tab" aria-selected={tab === 'open'} class:on={tab === 'open'} onclick={() => show('open')}>Open</button>
    <button role="tab" aria-selected={tab === 'history'} class:on={tab === 'history'} onclick={() => show('history')}>History</button>
  </div>
  {#if tab === 'open' && chips.length > 1}
    <div class="filters">
      <button class="pill" class:on={area === ''} onclick={() => (area = '')}>All</button>
      {#each chips as c (c)}<button class="pill" class:on={area === c} onclick={() => (area = area === c ? '' : c)}>{c}</button>{/each}
    </div>
  {/if}
  <div class="scroll">
    {#if list.length === 0}
      <p class="none">{tab === 'open' ? 'No open issues.' : 'No closed or converted issues yet.'}</p>
    {/if}
    {#each list as i (i.id)}
      <article class="item">
        <div class="ti">{i.title}</div>
        {#if location(i)}<div class="loc">{location(i)}</div>{/if}
        <div class="meta">{i.filer} · {age(i.filedAt)}</div>
        <footer>
          {#if tab === 'open'}
            <span class="by"></span>
            <button onclick={() => ongoal(i)}>Goal</button>
            <button onclick={() => onexport(i)}>Export</button>
            <button class="danger" onclick={() => oncloseissue(i)}>Close</button>
          {:else}
            <span class="by">{historyLabel(i)} · {age(i.changedAt)}</span>
            {#if i.githubUrl}<a href={i.githubUrl} target="_blank" rel="noreferrer">GitHub</a>{/if}
          {/if}
        </footer>
      </article>
    {/each}
  </div>
</aside>

<style>
  .issues { width: 360px; flex: none; background: var(--side); display: flex; flex-direction: column; min-height: 0; border-left: 1px solid var(--line-soft); }
  header { flex: none; display: flex; align-items: center; padding: 14px var(--s3) var(--s2) var(--s4); }
  h2 { flex: 1; margin: 0; font-size: var(--t-lead); font-weight: 600; display: flex; gap: 6px; align-items: baseline; }
  .n { color: var(--faint); font-weight: 500; font-size: var(--t-ui); }
  .x { color: var(--muted); width: 28px; height: 28px; border-radius: 6px; font-size: var(--t-ui); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { color: var(--ink); background: var(--hover); }
  }
  .x:active { background: var(--select); }
  .tabs { flex: none; display: flex; gap: 2px; padding: 0 var(--s3); border-bottom: 1px solid var(--line-soft); }
  .tabs button { padding: 7px 10px; font-size: var(--t-ui); color: var(--muted); border-bottom: 2px solid transparent; }
  .tabs button.on { color: var(--ink); border-color: var(--iris); }
  .filters { flex: none; display: flex; gap: 6px; padding: 10px var(--s4) 2px; flex-wrap: wrap; }
  .pill { font-size: var(--t-sm); padding: 3px 8px; border-radius: 20px; background: var(--raise); color: var(--muted); }
  .pill.on { background: var(--select); color: var(--ink); }
  .scroll { flex: 1; overflow-y: auto; padding: var(--s2) 10px var(--s3); }
  .none { margin: var(--s2) 6px; font-size: var(--t-ui); color: var(--faint); line-height: var(--leading); }
  .item { background: var(--main); border: 1px solid var(--line-soft); border-radius: 8px; padding: 10px var(--s3) var(--s2); margin-bottom: 10px; min-width: 0; }
  .ti { font-size: var(--t-body); font-weight: 500; line-height: var(--leading-tight); overflow-wrap: anywhere; }
  .loc { margin-top: var(--s1); font: var(--t-cap) var(--mono); color: var(--iris); overflow-wrap: anywhere; }
  .meta { margin-top: var(--s1); font-size: var(--t-sm); color: var(--faint); }
  footer { display: flex; align-items: center; gap: var(--s1); margin-top: var(--s2); padding-top: 6px; border-top: 1px solid var(--line-soft); }
  .by { flex: 1; font-size: var(--t-sm); color: var(--faint); }
  footer button, footer a { font-size: var(--t-sm); color: var(--muted); padding: 3px 9px; border-radius: 5px; background: var(--raise); text-decoration: none; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    footer button:hover, footer a:hover { color: var(--ink); background: var(--hover); }
  }
  footer button:active, footer a:active { background: var(--select); }
  footer button.danger { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    footer button.danger:hover { color: var(--coral); }
  }
</style>
