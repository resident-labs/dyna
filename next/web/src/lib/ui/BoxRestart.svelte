<script lang="ts">
  import { confirmRestart } from "../session-confirms.ts";
  // Restart for a failed box, as session.restart does it: the shared confirm says what it costs first. A restart that
  // lands takes the failure, and this control with it, away through box.changed, so it stays disabled until then; a second
  // click could only meet a false conflict. A conflict means no restart is left to offer, so only its line stays. Either
  // way onclosed moves focus on.
  let { session, name, onclosed }: { session: string; name: string; onclosed: () => void } = $props();
  let started = $state(false), refused = $state(false);
  async function ask(event: MouseEvent & { currentTarget: HTMLButtonElement }) {
    if (await confirmRestart(session, name, event.currentTarget, () => { refused = true; })) { started = !refused; onclosed(); }
  }
</script>

{#if refused}<p class="failure" role="alert">This box can no longer restart.</p>
{:else}<button type="button" aria-haspopup="dialog" disabled={started} onclick={event => void ask(event)}>Restart box</button>{/if}

<style>
  p { margin: 0; }
  button { height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--hover); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  button:disabled { opacity: 0.6; cursor: default; }
  .failure { color: var(--danger); font-size: 12.5px; }
  @media (max-width: 640px) { button { height: 40px; } }
</style>
