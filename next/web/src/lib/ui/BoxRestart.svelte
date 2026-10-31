<script lang="ts">
  import { tick } from "svelte";
  import { APIError, call } from "../client.ts";
  // Restart for a failed box, as session.restart does it: the owner reads what it costs first. Like v1's confirms, the
  // question opens in place with focus on Cancel, so a stray Enter or Space never runs it. A restart that lands takes
  // the failure, and this control with it, away through box.changed; onrestarted then moves focus on.
  let { session, onrestarted }: { session: string; onrestarted: () => void } = $props();
  let asking = $state(false), busy = $state(false), failure = $state("");
  let opener: HTMLButtonElement | undefined = $state();
  let cancelButton: HTMLButtonElement | undefined = $state();
  async function ask() { asking = true; failure = ""; await tick(); cancelButton?.focus(); }
  async function cancel() { asking = false; await tick(); opener?.focus(); }
  async function restart() {
    busy = true;
    failure = "";
    await call("session.restart", { session }).then(onrestarted, (error: unknown) => {
      failure = error instanceof APIError && error.detail.code === "conflict" ? "This box can no longer restart." : "The restart did not start. Try again.";
    });
    busy = false;
  }
</script>

{#if asking}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions (Escape leaves the question from either button) -->
  <div class="ask" role="group" aria-label="Restart this box" onkeydown={event => { if (event.key === "Escape") { event.stopPropagation(); void cancel(); } }}>
    <p>Restart replaces this box and deletes its old volume. Work that was not pushed is lost. The session, its grants and its history stay.</p>
    <div class="actions">
      <button type="button" class="danger" disabled={busy} onclick={() => void restart()}>Restart box</button>
      <button type="button" bind:this={cancelButton} disabled={busy} onclick={() => void cancel()}>Cancel</button>
    </div>
  </div>
{:else}<button type="button" bind:this={opener} onclick={() => void ask()}>Restart box</button>{/if}
{#if failure}<p class="failure" role="alert">{failure}</p>{/if}

<style>
  .ask { display: grid; gap: 8px; max-width: 560px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; }
  p { margin: 0; }
  .actions { display: flex; gap: 8px; }
  button { height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--hover); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  button:disabled { opacity: 0.6; cursor: default; }
  .danger { border-color: color-mix(in srgb, var(--danger) 45%, var(--line)); color: var(--danger); }
  .failure { margin-top: 6px; color: var(--danger); font-size: 12.5px; }
  @media (max-width: 640px) { button { height: 40px; } }
</style>
