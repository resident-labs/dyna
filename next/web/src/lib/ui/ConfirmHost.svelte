<script lang="ts">
  import Modal from "./Modal.svelte";
  import { confirmations, type Asking } from "../modal.ts";
  // The shared confirm, mounted once at the app's root, so a confirm asked from a menu that has closed, or from a pane
  // that is inert behind a sheet, still shows. Focus starts on Cancel, so a stray Enter or Space never runs it. The last
  // confirm stays drawn while its modal unmounts, so nothing reads a confirm that has gone.
  let asking: Asking | undefined = $state();
  let last: Asking | undefined = $state();
  $effect(() => confirmations.subscribe(() => { const next = confirmations.current; asking = next; if (next) last = next; }));
</script>

{#if asking && last}{#key asking.id}
  <Modal title={last.title} fit busy={last.busy} opener={last.opener} initial="[data-cancel]" onclose={() => confirmations.cancel()}>
    {#if last.message}<p>{last.message}</p>{/if}
    {#if last.note}<p class="note">{last.note}</p>{/if}
    {#if last.problem}<p class="problem" role="alert">{last.problem}</p>{/if}
    {#snippet actions()}
      <button type="button" class="btn" data-cancel disabled={last?.busy} onclick={() => confirmations.cancel()}>Cancel</button>
      <button type="button" class="btn" class:danger={last?.danger !== false} class:fill={last?.danger === false} disabled={last?.busy} onclick={() => void confirmations.accept()}>{last?.action}</button>
    {/snippet}
  </Modal>
{/key}{/if}

<style>
  p { margin: 0; }
  p + p { margin-top: 8px; }
  .note { color: var(--muted); font-size: 12.5px; }
  .problem { color: var(--danger); font-size: 13px; font-weight: 500; }
</style>
