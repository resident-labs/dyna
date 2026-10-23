<script lang="ts">
  // One change's own rules, opened from its "Uses room rules" link. The
  // change can override the room rules with a recorded reason (see
  // ChangePolicy). The room's own review settings live in Room settings.
  import { dialog as modal } from '../ui/dialog.ts'
  import ChangeDetail from './ChangeDetail.svelte'
  import ChangePolicy from './ChangePolicy.svelte'
  import { api } from '../../api.js'
  import { normalizePolicy, type NormalizedPolicy } from './pr.ts'
  import type { ChangePolicy as RoomPolicy } from '../api/change.ts'
  import type { Row } from './prlist.ts'
  import { errorText } from '../errorText.ts'

  interface Props {
    roomId: string
    change: Row
    onclose?: () => void
    onchange?: () => void | Promise<void>
  }
  let { roomId, change, onclose = () => {}, onchange = () => {} }: Props = $props()
  let policy = $state<NormalizedPolicy | null>(null)
  let error = $state('')

  $effect(() => {
    api.roomChangePolicy(roomId).then((p: RoomPolicy) => (policy = normalizePolicy(p)), (e: unknown) => (error = errorText(e)))
  })
</script>

<div class="backdrop">
  <aside class="drawer" use:modal={{ close: onclose }} tabindex="-1" role="dialog" aria-modal="true" aria-label="Rules for this change">
    <header>
      <h2>Rules for this change</h2>
      <p>The room rules are in Room settings.</p>
      <button class="x" onclick={onclose} aria-label="Close">
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
      </button>
    </header>
    {#if !change.synthetic}
      <section class="sec">
        <ChangeDetail row={change} {roomId}>
          {#snippet children(detail, detailError, loading, refresh)}
            {#if loading}<p>Loading the rules…</p>{/if}
            {#if detailError}<p class="err">{detailError}</p>{/if}
            {#if detail}
              <ChangePolicy change={detail} {roomId} room={policy} onchange={() => { refresh(); return onchange() }} />
            {/if}
          {/snippet}
        </ChangeDetail>
      </section>
    {/if}
    {#if error}<p class="err">{error}</p>{/if}
  </aside>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.5); z-index: 60; display: flex; justify-content: flex-end; }
  .drawer { position: relative; width: min(520px, 100%); height: 100%; overflow-y: auto; background: var(--side); border-left: 1px solid var(--line); padding: 18px 20px 20px; outline: none; box-shadow: -16px 0 48px rgba(0, 0, 0, 0.4); }
  header { padding-right: 32px; }
  h2 { margin: 0 0 2px; font-size: 18px; font-weight: 600; }
  header p { margin: 0 0 6px; color: var(--faint); font-size: 12.5px; }
  .x { position: absolute; top: 14px; right: 14px; width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center; color: var(--muted); }
  .x:hover { background: var(--hover); color: var(--ink); }
  .sec { border-top: 1px solid var(--line-soft); margin-top: 12px; padding-top: 12px; }
  .err { margin: 8px 0 0; color: var(--coral); font-size: 12.5px; }
  @media (max-width: 760px) {
    .x { width: 44px; height: 44px; top: 8px; right: 8px; }
  }
</style>
