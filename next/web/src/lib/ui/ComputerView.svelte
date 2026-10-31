<script lang="ts">
  import Icon from "./Icon.svelte";
  let { session, name, onback }: { session: string; name: string; onback: () => void } = $props();
  let visible = $state(document.visibilityState === "visible");
  // A stopped computer closes this view, so an error here is a broken stream. The owner's retry
  // or the page becoming visible again tries once more; no timer does.
  let failed = $state(false);
  // Chromium may keep a multipart request open until a removed <img> is garbage-collected, so the
  // image drops its stream before it leaves. The error that the clearing fires is not a failure.
  function watch(image: HTMLImageElement) {
    let closing = false;
    image.addEventListener("error", () => { if (!closing) failed = true; });
    return () => { closing = true; image.src = "data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=="; };
  }
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible) failed = false; }} />
<header class="label"><button type="button" class="back" aria-label="Back to sessions" title="Back to sessions" onclick={onback}
  {@attach button => button.focus()}><Icon name="chevron" /></button>{name}'s computer{#if !failed}<span class="live">Live</span>{/if}</header>
<!-- The image streams only while the owner can see it: this view open, the page visible. -->
<div class="screen">
  {#if failed}<p>The live view stopped. <button type="button" class="retry" onclick={() => { failed = false; }}>Try again</button></p>
  {:else if visible}<img src={`/v1/computers/watch?session=${encodeURIComponent(session)}`} alt={`${name}'s screen, live`} {@attach watch} />{/if}
</div>

<style>
  header { gap: 6px; padding-left: 8px; }
  .back { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; cursor: pointer; }
  .back:hover { background: var(--hover); color: var(--text); }
  .back:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .back :global(svg) { transform: rotate(90deg); }
  .live { margin-left: auto; font-weight: 400; }
  /* The image keeps its ratio inside a 16:9 box, letterboxed on the panel. */
  .screen { position: relative; display: grid; place-items: center; aspect-ratio: 16 / 9; margin: 12px; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
  p { margin: 0 12px; color: var(--muted); font-size: 13px; text-align: center; }
</style>
