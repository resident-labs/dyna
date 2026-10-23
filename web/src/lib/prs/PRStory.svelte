<script lang="ts">
  // The story of one change inside a session's PR card: its title, one line
  // of facts, the status box (steps, one sentence, the action bar) and a
  // footer of small buttons. The room's PR page has its own header and merge
  // box (PRDetail, MergeBox).
  //
  // Props:
  //   pr, names, facts, merge, review, room, roomId: as in PRCard
  //   onuntrack, onresume, ontrack, onrules, onreviewer, onchange, onsession
  //   onread    the "Read the review" button
  //   children  snippet under the status box; actions: extra footer buttons
  import { openExternal } from '../../api.js'
  import { ago } from '../ui/format.ts'
  import StatusBox from './StatusBox.svelte'
  import { isLocal, changeName, changeTitle } from './pr.ts'
  import { factsLine } from './prlist.ts'
  import type { StoryProps } from './props.ts'

  let { pr, names = {}, facts = null, merge = null, review = true, room = null, roomId = '', onuntrack = null, onresume = null, ontrack = null, onrules = null, onreviewer = null, onchange = () => {}, onread = null, onsession = null, children: more = null, actions = null }: StoryProps = $props()
  let local = $derived(isLocal(pr))
  let busy = $state(false)
  async function untrack() {
    busy = true
    try {
      await onuntrack?.(pr)
    } finally {
      busy = false
    }
  }
</script>

<div class="story">
  <header>
    <div class="title">
      <span class="num mono">{changeName(pr)}</span>
      <h3>{changeTitle(pr)}</h3>
    </div>
    {#if !local && pr.url}<button class="gh" onclick={() => openExternal(pr.url)} title="Open on GitHub">
      <svg viewBox="0 0 20 20" width="15" height="15" aria-hidden="true"><path d="M8 4.5H5a1 1 0 0 0-1 1v9.5a1 1 0 0 0 1 1h9.5a1 1 0 0 0 1-1V12M11 4h5v5M16 4l-7 7" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
      <span>GitHub</span>
    </button>{/if}
  </header>
  <p class="sub">{facts ?? factsLine(pr, { names, ago })}</p>
  <StatusBox change={pr} {merge} {review} {room} {roomId} {onresume} {ontrack} {onrules} {onchange} onsession={onreviewer} onreview={onread} />
  {#if more}<div class="extra">{@render more()}</div>{/if}
  <footer>
    <span class="spacer"></span>
    {#if onsession && pr.authorSession}<button class="small" onclick={() => onsession(pr)}>Open session</button>{/if}
    {#if actions}{@render actions()}{/if}
    {#if onuntrack && !local && pr.state === 'open' && pr.status !== 'untracked'}<button class="small" onclick={untrack} disabled={busy}>Untrack</button>{/if}
  </footer>
</div>

<style>
  .story { display: flex; flex-direction: column; min-width: 0; }
  header { display: flex; align-items: flex-start; gap: 12px; }
  .title { flex: 1; min-width: 0; display: flex; align-items: baseline; gap: 8px; }
  .num { color: var(--muted); font-size: 13px; flex: none; }
  h3 { margin: 0; font-size: 16px; font-weight: 600; line-height: 1.3; overflow-wrap: anywhere; }
  .gh { flex: none; display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 5px 10px; }
  .gh:hover { background: var(--raise-hover); color: var(--ink); }
  .sub { margin: 4px 0 12px; font-size: 12.5px; color: var(--faint); overflow-wrap: anywhere; }
  .extra { margin: 4px 0 10px; }
  footer { display: flex; align-items: center; gap: 8px; padding-top: 8px; margin-top: 8px; border-top: 1px solid var(--line-soft); }
  .spacer { flex: 1; }
  .small { font-size: 12.5px; font-weight: 600; color: var(--muted); border-radius: 6px; padding: 5px 10px; }
  .small:hover { color: var(--ink); background: var(--hover); }
  button:disabled { opacity: 0.5; cursor: default; }
  @media (max-width: 760px) {
    .gh { min-height: 44px; padding: 0 12px; }
    .gh span { display: none; }
    .small { min-height: 44px; padding: 0 14px; }
  }
</style>
