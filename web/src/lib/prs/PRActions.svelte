<script lang="ts">
  // The action bar of a change: the one main button from statusBox (resume
  // or track) and the review buttons.
  //
  // Props:
  //   change, box (the statusBox answer)
  //   onresume, ontrack, onreview, onsession: the same actions as StatusBox

  import type { Row, StatusBox } from './prlist.ts'
  import type { ChangeAction, TrackAction } from './props.ts'

  interface Props {
    change: Row
    box: StatusBox
    onresume?: ChangeAction | null
    ontrack?: TrackAction | null
    onreview?: (() => void) | null
    onsession?: (() => void) | null
  }
  let { change, box, onresume = null, ontrack = null, onreview = null, onsession = null }: Props = $props()
  let busy = $state(false)
  async function resume() {
    busy = true
    try {
      await onresume?.(change)
    } finally {
      busy = false
    }
  }
</script>

<div class="act">
  {#if box.button?.id === 'resume' && onresume}
    <button class="btn p" onclick={resume} disabled={busy}>{busy ? 'Resuming…' : box.button.label}</button>
  {:else if box.button?.id === 'track' && ontrack}
    <button class="btn p" onclick={(e) => ontrack(change, e)}>{box.button.label}</button>
  {/if}
  {#if box.review && onreview}<button class="btn" onclick={onreview}>Read the review</button>{/if}
  {#if box.session && onsession}<button class="btn" onclick={onsession}>Watch the reviewer</button>{/if}
</div>

<style>
  .act { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
  .act:empty { display: none; }
  .btn { padding: 6px 14px; border-radius: 7px; font-weight: 600; font-size: 13px; background: var(--raise); color: var(--ink-2); }
  .btn:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .btn.p { background: var(--gold); color: #18161d; }
  .btn:disabled { opacity: 0.5; cursor: default; }
  @media (max-width: 760px) { .btn { min-height: 44px; padding: 0 14px; } }
</style>
