<script lang="ts">
  import { tick } from "svelte";
  import { APIError, call } from "../client.ts";
  // Restart for a failed box, as session.restart does it: the owner reads what it costs first. Like v1's confirms, the
  // question opens in place with focus on Cancel, so a stray Enter or Space never runs it. A restart that lands takes
  // the failure, and this control with it, away through box.changed, so it stays busy until then; a second click could
  // only meet a false conflict. A conflict means no restart is left to offer, so only its line stays. Either way
  // onclosed moves focus on, as the buttons are gone.
  let { session, onclosed }: { session: string; onclosed: () => void } = $props();
  let asking = $state(false), busy = $state(false), refused = $state(false), failure = $state("");
  let opener: HTMLButtonElement | undefined = $state();
  let cancelButton: HTMLButtonElement | undefined = $state();
  async function ask(tapped: boolean) { asking = true; failure = ""; await tick(); cancelButton?.focus({ focusVisible: !tapped }); }
  async function cancel(tapped: boolean) { asking = false; await tick(); opener?.focus({ focusVisible: !tapped }); }
  async function restart() {
    busy = true;
    failure = "";
    await call("session.restart", { session }).then(onclosed, (error: unknown) => {
      busy = false;
      refused = error instanceof APIError && error.detail.code === "conflict";
      failure = refused ? "This box can no longer restart." : "The restart did not start. Try again.";
      if (refused) { asking = false; onclosed(); }
    });
  }
</script>

{#if asking}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions (Escape leaves the question from either button) -->
  <div class="ask" role="group" aria-label="Restart this box" onkeydown={event => { if (event.key === "Escape") { event.stopPropagation(); if (!busy) void cancel(false); } }}>
    <p>Restart replaces this box. Its old volume and any work that was not pushed are lost. The session, its grants and its history stay.</p>
    <div class="actions">
      <button type="button" class="danger" disabled={busy} onclick={() => void restart()}>Restart box</button>
      <button type="button" bind:this={cancelButton} disabled={busy} onclick={event => void cancel(event.detail > 0)}>Cancel</button>
    </div>
  </div>
{:else if !refused}<button type="button" bind:this={opener} onclick={event => void ask(event.detail > 0)}>Restart box</button>{/if}
{#if failure}<p class="failure" role="alert">{failure}</p>{/if}

<style>
  button + .failure, .ask + .failure { margin-top: 6px; }
  .ask { display: grid; gap: 8px; max-width: 560px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; }
  p { margin: 0; }
  .actions { display: flex; gap: 8px; }
  button { height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--hover); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  button:disabled { opacity: 0.6; cursor: default; }
  .danger { border-color: color-mix(in srgb, var(--danger) 45%, var(--line)); color: var(--danger); }
  .failure { color: var(--danger); font-size: 12.5px; }
  @media (max-width: 640px) { button { height: 40px; } }
</style>
