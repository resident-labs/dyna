<script lang="ts">
  import type { CloseOutcome } from '../api/room.ts'
  import { closeRows, type CloseChoice, type CloseRow } from './closeOutcomes.ts'

  // The result of a session close: one row per typed fact the engine returned.
  // The rows word themselves from kind, result and failure, so no engine text
  // reaches this view.
  interface Props {
    name: string
    outcomes: CloseOutcome[]
    onclose: () => void
    onchoose?: (choice: CloseChoice, row: CloseRow) => void
  }

  let { name, outcomes, onclose, onchoose = () => {} }: Props = $props()
  let rows = $derived(closeRows(outcomes))
</script>

<section class="close-result" role="status" aria-label="Session close result">
  <header>
    <strong>{name} is closed.</strong>
    <button class="dismiss" onclick={onclose} aria-label="Dismiss the close result">×</button>
  </header>
  {#if rows.length}
    <ul>
      {#each rows as row (row.key)}
        <li class:failed={row.failed}>
          <span class="subject">{row.subject}</span>
          {#if row.result}<span class="result">{row.result}</span>{/if}
          {#if row.failure}<span class="failure">{row.failure}</span>{/if}
          {#if row.choice}
            {@const choice = row.choice}
            <button class="choice" onclick={() => onchoose(choice.action, row)}>{choice.label}</button>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="empty">It held no work to hand over.</p>
  {/if}
</section>

<style>
  /* The panel floats above the page, clear of the composer and the toasts. */
  .close-result {
    position: fixed;
    left: 50%;
    bottom: 84px;
    transform: translateX(-50%);
    z-index: 70;
    width: max-content;
    max-width: min(640px, calc(100vw - 32px));
    max-height: 50vh;
    overflow: auto;
    overflow-wrap: anywhere;
    padding: 10px 14px;
    border: 1px solid var(--line-soft);
    border-radius: var(--r-sm);
    background: var(--rail);
    color: var(--ink-2);
    font-size: var(--t-ui);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
  }
  header { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .dismiss { background: none; border: 0; color: var(--muted); font-size: var(--t-ui); cursor: pointer; padding: 0 2px; transition: color var(--motion-fast); }
  .dismiss:hover { color: var(--ink-2); }
  ul { margin: 8px 0 0; padding: 0; list-style: none; display: grid; gap: 6px; }
  li { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  li.failed .failure { color: var(--coral); }
  .subject { color: var(--ink-2); font-weight: 600; }
  .result, .failure { color: var(--muted); }
  .choice { margin-left: auto; background: none; border: 1px solid var(--line-soft); border-radius: var(--r-xs); color: var(--ink-2); font-size: var(--t-ui); padding: 2px 8px; cursor: pointer; }
  .choice:hover { border-color: var(--iris-edge); }
  .empty { margin: 8px 0 0; color: var(--muted); }
  @media (max-width: 720px) {
    .close-result { bottom: calc(96px + env(safe-area-inset-bottom)); }
  }
</style>
