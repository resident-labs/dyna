<script lang="ts">
  // A session's pull requests, from the PR watcher: each open PR as a
  // PRCard, ended ones (merged, closed, untracked) in a short list.
  import { openExternal } from '../../api.js'
  import PRChip from './PRChip.svelte'
  import PRCard from './PRCard.svelte'
  import { isOpen } from './pr.ts'
  import type { Names, Row } from './prlist.ts'
  import type { ChangeAction } from './props.ts'

  interface Props {
    prs?: Row[]
    names?: Names
    error?: string
    onuntrack: ChangeAction
    onresume: ChangeAction
  }
  // names maps session ids to member names, for the reviewer and lead.
  let { prs = [], names = {}, error = '', onuntrack, onresume }: Props = $props()

  let open = $derived(prs.filter(isOpen))
  let ended = $derived(prs.filter((p) => !isOpen(p)))
  let busy = $state('')

  async function act(fn: ChangeAction, pr: Row) {
    busy = pr.key ?? ''
    try {
      await fn(pr)
    } finally {
      busy = ''
    }
  }
</script>

<section class="prs">
  {#if error}
    <p class="err">{error}</p>
  {:else if !prs.length}
    <div class="empty">
      <strong>No tracked pull requests</strong>
      <p>When this session opens a PR, it runs <span class="mono">dyna git pr track &lt;number&gt;</span>. Dyna then watches CI and review, and the PR shows here.</p>
    </div>
  {/if}

  {#each open as pr (pr.key)}
    <PRCard {pr} {names} {onuntrack} {onresume} />
  {/each}

  {#if ended.length}
    <h4 class="endh">Earlier<span>{ended.length}</span></h4>
    <ul class="ended">
      {#each ended as pr (pr.key)}
        <li>
          <PRChip {pr} />
          <span class="etitle">{pr.title || `${pr.repo} #${pr.number}`}</span>
          {#if pr.status === 'untracked' && onresume}<button class="mini" onclick={() => act(onresume, pr)} disabled={busy === pr.key}>Track again</button>{/if}
          <button class="mini" onclick={() => openExternal(pr.url)}>GitHub</button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .prs { flex: 1; overflow-y: auto; padding: 16px 20px 32px; min-height: 0; display: flex; flex-direction: column; gap: 12px; }
  .prs > :global(*) { width: 100%; max-width: 880px; }
  .err { color: var(--coral); }
  .empty { margin: 24px auto; max-width: 440px; text-align: center; background: var(--side); border: 1px dashed var(--line); border-radius: 12px; padding: 24px; }
  .empty p { color: var(--muted); font-size: 14px; margin: 6px 0 0; }
  h4 { display: flex; gap: 8px; align-items: baseline; margin: 8px 0 0; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
  h4 span { font-weight: 500; letter-spacing: 0; color: var(--faint); text-transform: none; }
  .ended { list-style: none; margin: 0; padding: 0; background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; overflow: hidden; }
  .ended li { display: flex; align-items: center; gap: 10px; padding: 8px 12px; }
  .ended li + li { border-top: 1px solid var(--line-soft); }
  .etitle { flex: 1; min-width: 0; font-size: 13.5px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mini { font-size: 12.5px; font-weight: 600; color: var(--muted); border-radius: 6px; padding: 5px 10px; }
  .mini:hover { color: var(--ink); background: var(--hover); }
  button:disabled { opacity: 0.5; cursor: default; }
  @media (max-width: 760px) {
    .prs { padding: 12px 10px 24px; }
    .mini { min-height: 44px; padding: 0 14px; }
  }
</style>
