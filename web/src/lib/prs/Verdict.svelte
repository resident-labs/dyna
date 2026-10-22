<script lang="ts">
  // The reviewer's verdict on a change and its summary. PRCard draws it under
  // the status box; the PR screen draws it first in its Review tab. el is the
  // box, so a button can scroll to it.
  import { markdown } from '../ui/format.js'
  import { shortSha, verdictMeta, isLocal, type Facts } from './pr.ts'
  import type { Names } from './prlist.ts'

  let { pr, names = {}, el = $bindable(null) }: { pr: Facts & { summary?: string }; names?: Names; el?: HTMLElement | null } = $props()
  let vm = $derived(verdictMeta(pr.verdict))
  // Session names only; an id the room does not know reads as a role.
  const who = (id: string | undefined, role = 'the reviewer') => (id && names[id]) || role
</script>

{#if vm}
  <div class="verdict {vm.tone}" bind:this={el}>
    <div class="vh">
      <strong>{vm.label}</strong>
      {#if vm.note}<span class="vnote">{vm.note}</span>{/if}
      {#if !isLocal(pr)}<span>{pr.reviewUrl || pr.reviewPublishing === 'github' ? 'Published on GitHub' : 'Dyna only'}</span>{/if}
      <span>{[`by ${who(pr.reviewerSession)}`, `round ${pr.reviewRound || 1}`, pr.reviewHead ? `at ${shortSha(pr.reviewHead)}` : ''].filter(Boolean).join(' · ')}</span>
    </div>
    {#if pr.summary}<div class="prose small">{@html markdown(pr.summary)}</div>{/if}
  </div>
{/if}

<style>
  .verdict { border-radius: 8px; padding: 10px 12px; margin: 12px 0; background: var(--main); border-left: 3px solid var(--line); }
  .verdict.ok { border-left-color: var(--sage); }
  .verdict.warn { border-left-color: var(--gold); }
  .vh { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 8px; margin-bottom: 4px; }
  .verdict.ok strong { color: var(--sage); }
  .verdict.warn strong { color: var(--gold); }
  .vh span { font-size: 12.5px; color: var(--faint); }
  .small { font-size: 13.5px; }
</style>
