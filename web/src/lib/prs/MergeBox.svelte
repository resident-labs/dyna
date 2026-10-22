<script lang="ts">
  // The merge box at the end of a PR's conversation, as on GitHub: a row for
  // the checks, the review and the merge (mergeBox in prlist.js), then the
  // action bar.
  //
  // Props:
  //   change, box (the mergeBox answer)
  //   onchecks opens the Checks tab; onread shows the latest review
  //   onresume, ontrack, onreviewer: PRActions' buttons
  //   onauthor opens the author session; onrules the PR's rules; onuntrack stops watching
  //   actions  extra buttons, such as Ask an agent
  import PRActions from './PRActions.svelte'
  import type { Snippet } from 'svelte'
  import { isLocal } from './pr.ts'
  import type { mergeBox, Row } from './prlist.ts'
  import type { ChangeAction, TrackAction } from './props.ts'

  interface Props {
    change: Row
    box: ReturnType<typeof mergeBox>
    onchecks?: (() => void) | null
    onread?: (() => void) | null
    onresume?: ChangeAction | null
    ontrack?: TrackAction | null
    onreviewer?: (() => void) | null
    onauthor?: ChangeAction | null
    onrules?: (() => void) | null
    onuntrack?: ChangeAction | null
    actions?: Snippet | null
  }
  let { change, box, onchecks = null, onread = null, onresume = null, ontrack = null, onreviewer = null, onauthor = null, onrules = null, onuntrack = null, actions = null }: Props = $props()
  const icons: Record<string, string> = { ok: '✓', bad: '✕', run: '●' }
  let busy = $state(false)
  async function untrack() {
    busy = true
    try {
      await onuntrack?.(change)
    } finally {
      busy = false
    }
  }
</script>

<section class="mbox" aria-label="Merge">
  {#each box.rows as r (r.id)}
    {@const go = r.link === 'checks' ? onchecks : r.link === 'review' ? onread : null}
    <div class="r">
      <span class="ic {r.icon}" aria-hidden="true">{icons[r.icon] ?? (r.id === 'merge' ? '3' : '·')}</span>
      <span class="what"><b>{r.title}</b>{#if r.note}<small>{r.note}</small>{/if}</span>
      {#if go}<button class="go" onclick={go}>{r.link === 'checks' ? 'Show checks' : 'Read the review'}</button>{/if}
    </div>
  {/each}
  <div class="bar">
    <PRActions {change} box={box.status} {onresume} {ontrack} onreview={onread} onsession={onreviewer} />
    <span class="spacer"></span>
    {#if onauthor && change.authorSession}<button class="small" onclick={() => onauthor?.(change)}>Open the author session</button>{/if}
    {#if actions}{@render actions()}{/if}
    {#if onrules && change.state === 'open'}<button class="small" onclick={onrules}>Rules for this {isLocal(change) ? 'change' : 'PR'}</button>{/if}
    {#if onuntrack && !isLocal(change) && change.state === 'open' && change.status !== 'untracked'}<button class="small" onclick={untrack} disabled={busy}>Untrack</button>{/if}
  </div>
</section>

<style>
  .mbox { margin: var(--s5) 0 var(--s2); border: 1px solid var(--line); border-radius: var(--r-sm); overflow: hidden; }
  .r { display: flex; gap: var(--s3); align-items: flex-start; padding: var(--s3) var(--s4); border-top: 1px solid var(--line-soft); }
  .r:first-child { border-top: 0; }
  .ic { width: 26px; height: 26px; border-radius: 50%; flex: none; display: grid; place-items: center; font-size: var(--t-sm); font-weight: 600; background: var(--raise); color: var(--muted); }
  .ic.ok { background: rgba(151, 201, 149, 0.15); color: var(--sage); }
  .ic.run { background: rgba(230, 192, 123, 0.15); color: var(--gold); }
  .ic.bad { background: rgba(239, 138, 128, 0.15); color: var(--coral); }
  .what { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .what b { font-size: var(--t-body); font-weight: 600; }
  .what small { font-size: var(--t-sm); color: var(--muted); overflow-wrap: anywhere; }
  .go { flex: none; font-size: var(--t-sm); font-weight: 600; color: var(--iris); padding: 2px 0; }
  .go:hover { text-decoration: underline; }
  .bar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); padding: var(--s3) var(--s4); background: var(--side); border-top: 1px solid var(--line-soft); }
  .bar :global(.act) { margin-top: 0; }
  .spacer { flex: 1; }
  .small { font-size: 12.5px; font-weight: 600; color: var(--muted); border-radius: 6px; padding: 5px 10px; }
  .small:hover:not(:disabled) { color: var(--ink); background: var(--hover); }
  .small:disabled { opacity: 0.5; }
  @media (max-width: 760px) { .r { padding: var(--s3); } .small, .go { min-height: 44px; } }
</style>
