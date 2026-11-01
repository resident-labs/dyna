<script lang="ts">
  import { APIError, call } from "../client.ts";
  // The owner's answer to a waiting question. The engine's events take the question out of waiting; onanswered is for a
  // list that wants to say so at once. Enter sends and Shift+Enter adds a line, as in the composer (spec 5.13).
  let { id, name, onanswered }: { id: string; name: string; onanswered?: (text: string) => void } = $props();
  let text = $state(""), busy = $state(false), failure = $state("");
  async function send() {
    const answer = text.trim();
    if (!answer || busy) return;
    busy = true;
    failure = "";
    await call("question.answer", { id, text: answer }).then(() => { text = ""; onanswered?.(answer); }, (error: unknown) => {
      failure = error instanceof APIError && error.detail.code === "conflict" ? "This question is no longer waiting." : "Your answer was not saved. Try again.";
    });
    busy = false;
  }
  function keys(event: KeyboardEvent) {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    void send();
  }
</script>
<form onsubmit={event => { event.preventDefault(); void send(); }}>
  <textarea rows="1" aria-label="Answer to {name}" placeholder="Answer {name}" bind:value={text} disabled={busy} onkeydown={keys}></textarea>
  <button type="submit" disabled={busy || !text.trim()}>Answer</button>
</form>
{#if failure}<p role="alert">{failure}</p>{/if}
<style>
  form { display: flex; align-items: flex-start; gap: 8px; min-width: 0; width: 100%; }
  textarea { flex: 1; min-width: 0; min-height: 30px; max-height: 160px; padding: 5px 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 13px; line-height: 18px; resize: vertical; field-sizing: content; }
  textarea:focus-visible { border-color: var(--focus); outline: none; box-shadow: 0 0 0 1px var(--focus); }
  button { flex: none; height: 30px; padding: 0 12px; border: 1px solid var(--focus); border-radius: 6px; background: var(--focus); color: var(--bg); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  button:hover:not(:disabled) { filter: brightness(1.08); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  :disabled { opacity: 0.6; cursor: default; }
  p { margin: 4px 0 0; color: var(--muted); font-size: 12.5px; }
  @media (max-width: 640px), (hover: none) { textarea { min-height: 40px; padding: 10px; } button { height: 40px; } }
</style>
