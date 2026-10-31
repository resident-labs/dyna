<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { redeemPair, type PairResult } from "../client.ts";
  import { cleanCode, isFullCode, isPhone, showCode, thisBrowser } from "../devices.ts";
  import Icon from "./Icon.svelte";
  // The first screen on a new device. A link fills the code, and one tap spends it: v1 spent it on opening the page, so
  // a person could not check what they opened. A dead link falls back to the typed screen. Connect's sign-in button, which
  // returns with engine/connect, goes above the form.
  let { code = "" }: { code?: string } = $props();
  const guess = thisBrowser(navigator.userAgent, navigator.maxTouchPoints);
  // The link is read once; after it fails, the page is the typed screen with its code in the field.
  const link = untrack(() => code);
  let fromLink = $state(link !== ""), typed = $state(showCode(link));
  let name = $state(guess.name), renaming = $state(false);
  let busy = $state(false), result: PairResult | undefined = $state();
  let field: HTMLInputElement | undefined = $state();
  const errors: Partial<Record<PairResult, string>> = {
    wrong: "That code did not match. Check it and try again.",
    ended: "This code no longer works. Make a new one.",
    unreachable: "Dyna is not reachable. Try again.",
  };
  let error = $derived(result && errors[result]);
  onMount(() => { field?.focus(); });
  async function pair(event: SubmitEvent) {
    event.preventDefault();
    if (busy || !isFullCode(typed)) return;
    busy = true;
    const answer = await redeemPair(cleanCode(typed), name.trim() || guess.name);
    // A link names no typing error, so its failure is always a dead code.
    if (fromLink && answer === "wrong") result = "ended"; else result = answer;
    if (answer === "paired") { location.reload(); return; }
    fromLink = answer === "unreachable" && fromLink;
    busy = false;
  }
</script>

<main class="screen">
  <form onsubmit={pair}>
    <svg class="logo" viewBox="0 0 24 24" width="40" height="40" aria-hidden="true"><path d="M5 4h6.2c4.6 0 7.8 3.2 7.8 8s-3.2 8-7.8 8H5z" /></svg>
    {#if fromLink}
      <h1>Pair this {guess.named ? guess.name : "device"}?</h1>
      <p>You opened a pairing link for <code>{location.hostname}</code>. This device gets the access chosen in Settings.</p>
    {:else}
      <h1>Pair this device</h1>
      <p>On a signed-in device, open Settings, then Sharing & access, then Pair a device. Or run <code>dyna device pair</code> on the computer that runs Dyna.</p>
      <label>Pairing code
        <input class="code" class:bad={error} bind:this={field} value={typed} placeholder="XXXXX-XXXXX" autocomplete="one-time-code" autocapitalize="characters" autocorrect="off" spellcheck="false"
          aria-invalid={!!error} aria-describedby={error ? "pair-error" : undefined} oninput={event => { typed = showCode(event.currentTarget.value); event.currentTarget.value = typed; result = undefined; }} /></label>
    {/if}
    {#if error}<p class="error" id="pair-error" role="alert">{error}</p>{/if}
    <div class="named"><Icon name={isPhone(name) ? "phone" : "laptop"} size={20} />
      {#if renaming}<!-- svelte-ignore a11y_autofocus -->
        <input class="name" aria-label="Device name" maxlength="40" autofocus bind:value={name} onkeydown={event => { if (event.key === "Enter") { event.preventDefault(); renaming = false; } }} />
      {:else}<span>{name.trim() || guess.name}</span>{/if}
      <button type="button" class="link" onclick={() => { renaming = !renaming; }}>{renaming ? "Done" : "Rename"}</button></div>
    <button type="submit" class="big" disabled={busy || !isFullCode(typed)}>{busy ? "Pairing…" : "Pair"}</button>
    <p class="small">The pairing lasts 30 days from the last use. You can remove it in Settings, under Sharing & access.</p>
  </form>
</main>

<style>
  .screen { display: grid; place-items: start center; min-height: var(--app-height, 100dvh); padding: 200px 24px 24px; background: var(--bg); }
  form { display: grid; gap: 14px; width: min(360px, 100%); }
  .logo { fill: none; stroke: var(--text); stroke-width: 2; stroke-linejoin: round; }
  h1 { margin: 0; font-size: 24px; line-height: 1.2; }
  p { margin: 0; color: var(--muted); font-size: 14px; }
  p code { color: var(--text); font: 13px var(--mono); font-variant-ligatures: none; }
  label { display: grid; gap: 6px; font-size: 13px; font-weight: 500; }
  input { min-width: 0; border: 1px solid var(--firm); border-radius: 8px; background: var(--raised); color: var(--text); }
  .code { height: 48px; padding: 0 12px; font: 500 20px var(--mono); letter-spacing: 0.06em; text-align: center; text-transform: uppercase; font-variant-ligatures: none; }
  .code::placeholder { color: var(--muted); }
  .code.bad { border-color: var(--danger); }
  .error { color: var(--danger); font-size: 13px; font-weight: 500; }
  .named { display: flex; align-items: center; gap: 8px; min-height: 32px; font-size: 14px; }
  .named :global(svg) { color: var(--muted); }
  .name { flex: 1; height: 32px; padding: 0 8px; font: inherit; }
  .link { margin-left: auto; padding: 0; border: 0; background: none; color: var(--focus); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .big { height: 48px; border: 0; border-radius: 8px; background: var(--focus); color: var(--bg); font: inherit; font-size: 15px; font-weight: 600; cursor: pointer; }
  .big:disabled { opacity: 0.45; cursor: default; }
  .small { font-size: 12.5px; }
  /* 16 px text keeps iOS from zooming the page on focus. */
  @media (hover: none) { .link { min-height: 44px; } .name { height: 44px; font-size: 16px; } }
  @media (max-width: 640px) { .screen { padding-top: 72px; } }
</style>
