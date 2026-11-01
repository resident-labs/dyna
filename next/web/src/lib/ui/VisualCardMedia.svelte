<script lang="ts">
  import { untrack } from "svelte";
  import { cardMessage, visualReplyFailure, type VisualVersion } from "../visual.ts";
  import Icon from "./Icon.svelte";
  import { theme } from "../theme.svelte.ts";
  import { formatTime } from "../feed.ts";
  import { call } from "../client.ts";
  import { decodeEvent } from "../event.ts";
  import type { VisualSuperseded } from "../contract.gen.ts";

  let { versions, selected, room, send }: {
    versions: VisualVersion[]; selected: number; room: string;
    send: (text: string, event: string) => Promise<void>;
  } = $props();
  let version = $state(untrack(() => selected));
  let extra = $state<VisualVersion[]>([]);
  let allVersions = $derived([...extra, ...versions]);
  let current = $derived(allVersions.find(item => item.visual.version === version) ?? allVersions[0]);
  let latest = $derived(allVersions[0]);
  let older = $derived(current.visual.version < latest.visual.version);
  let menu = $state(false);
  let menuButton = $state<HTMLButtonElement>();
  let anchor: HTMLElement;
  function closeMenu() { menu = false; menuButton?.focus(); }
  function outside(event: PointerEvent) { if (menu && event.target instanceof Node && !anchor.contains(event.target)) closeMenu(); }
  let frame = $state<HTMLIFrameElement>();
  let pending = $state<string | null>(null);
  let loading = $state(true);
  let failed = $state(false);
  let navigated = $state(false);
  let sendFailed = $state(false);
  let sending = $state(false);
  let superseded = $state<VisualSuperseded>();
  let openFailed = $state(false);
  let attempt = $state(0);
  let loaded = 0;
  let lastSubmit = 0, lastOpen = 0;
  let url = $derived(`/v1/files/visual/${encodeURIComponent(current.event.id)}?theme=${theme().mode}`);
  let recipient = $state<string>();
  $effect(() => {
    const id = current.visual.id;
    let active = true;
    recipient = undefined;
    void call("events.list", { room, event: id, afterSeq: 0, limit: 1 }).then(page => {
      if (!active) return;
      const event = page.events?.[0] && decodeEvent(page.events[0]);
      if (event?.id === id && event.type === "post.created" && event.from.session) {
        recipient = page.names?.[event.from.session] ?? "the card's author";
      }
    }, () => {});
    return () => { active = false; };
  });

  $effect(() => {
    const source = url;
    attempt;
    const controller = new AbortController();
    loading = true; failed = false; navigated = false; loaded = 0; pending = null;
    void fetch(source, { signal: controller.signal }).then(response => {
      if (!response.ok) failed = true;
      void response.body?.cancel();
      loading = false;
    }, () => { if (!controller.signal.aborted) { failed = true; loading = false; } });
    return () => controller.abort();
  });
  function pick(value: number) { version = value; menu = false; pending = null; superseded = undefined; openFailed = false; }
  function reply(ok: boolean, error?: string) {
    frame?.contentWindow?.postMessage({ type: "dyna:submitted", ok, ...error && { error } }, "*");
  }
  function message(event: MessageEvent<unknown>) {
    if (!frame || event.source !== frame.contentWindow || event.origin !== "null") return;
    const data = cardMessage(event.data);
    if (!data || document.activeElement !== frame || navigator.userActivation?.isActive !== true) return;
    const now = Date.now();
    if (data.type === "open") {
      if (now - lastOpen >= 2000) { lastOpen = now; window.open(data.url, "_blank", "noopener,noreferrer"); }
      return;
    }
    if (now - lastSubmit < 2000) return;
    lastSubmit = now;
    if (older || superseded || !recipient) { reply(false, "unavailable"); return; }
    if (pending !== null) reply(false, "replaced");
    sendFailed = false; pending = data.text;
  }
  function cancel() { pending = null; sendFailed = false; reply(false, "cancelled"); }
  async function confirm() {
    if (pending === null || !recipient || older || superseded || sending) return;
    sending = true; sendFailed = false;
    const text = pending;
    await send(text, current.event.id).then(() => { pending = null; reply(true); }, error => {
      const result = visualReplyFailure(error);
      if (result.kind === "superseded") { superseded = result.visual; pending = null; reply(false, "superseded"); }
      else sendFailed = true;
    });
    sending = false;
  }
  async function openNew() {
    if (!superseded) return;
    const target = superseded;
    openFailed = false;
    const page = await call("events.list", { room, afterSeq: target.seq - 1, limit: 1 }).catch(() => undefined);
    const event = page?.events?.[0] && decodeEvent(page.events[0]);
    if (!event || event.type !== "post.created" || event.id !== target.event) { openFailed = true; return; }
    const visual = event.body.attachments?.find(file => file.visual?.version === target.version)?.visual;
    if (!visual || visual.id !== current.visual.id) { openFailed = true; return; }
    extra = [{ event, visual }, ...extra.filter(item => item.event.id !== event.id)];
    pick(visual.version);
  }
  function frameLoaded() {
    loaded++;
    // A second load is navigation initiated by the card. Stop it rather than displaying another document.
    if (loaded > 1) { navigated = true; pending = null; }
  }
</script>

<svelte:window onmessage={message} onpointerdown={outside} onblur={() => { menu = false; }} />
<section class="viewer" aria-label="Visual card" onkeydown={event => { if (event.key === "Escape" && menu) { event.stopPropagation(); event.preventDefault(); closeMenu(); } }}>
  <header>
    <strong title={current.visual.title}>{current.visual.title}</strong>
    <div class="anchor" bind:this={anchor}>
      {#if allVersions.length > 1}<button class="version" class:active={menu} bind:this={menuButton} type="button" aria-haspopup="menu" aria-expanded={menu} onclick={() => menu = !menu}>v{version} ⌄</button>
      {:else}<span class="version">v{version}</span>{/if}
      {#if menu}<div class="popover versions" role="menu">{#each allVersions as item (item.event.id)}<button type="button" role="menuitem" aria-current={item.visual.version === version ? "true" : undefined} onclick={() => { pick(item.visual.version); menuButton?.focus(); }}><span class="pill">v{item.visual.version}</span><span class="version-meta">{formatTime(item.event.at)}<small>{item.event.body.text.split(/\r?\n/)[0]}</small></span></button>{/each}</div>{/if}
    </div>
    <span class="spacer"></span>
    <a class="icon" href={url} target="_blank" rel="noopener noreferrer" title="Open in a new tab" aria-label="Open in a new tab"><Icon name="external" /></a>
  </header>
  {#if superseded}<div class="older" role="alert">This card has a newer version. <button type="button" onclick={openNew}>Open v{superseded.version}</button>{#if openFailed} The version did not open. Try again.{/if}</div>
  {:else if older}<div class="older">A newer loaded version is available. <button type="button" onclick={() => pick(latest.visual.version)}>Show v{latest.visual.version}</button></div>{/if}
  <div class="stage">
    {#if failed}<div class="failure">The card did not load. <button type="button" onclick={() => attempt++}>Retry</button></div>
    {:else if navigated}<div class="failure">The card tried to navigate away. Close it to stop the card.</div>
    {:else if !loading}
      {#key current.event.id + ":" + attempt}
        <iframe bind:this={frame} src={url} title={current.visual.title} sandbox="allow-scripts" referrerpolicy="no-referrer" onload={frameLoaded}></iframe>
      {/key}
    {/if}
  </div>
  {#if pending !== null}<div class="confirm" role="group" aria-label="Confirm what the card sends">
    <p><strong>{current.visual.title} v{version}</strong> asks to send this to <strong>{recipient}</strong>:</p><pre>{pending}</pre>
    {#if sendFailed}<p role="alert">The reply did not send. Try again.</p>{/if}
    <div><button class="send" type="button" onclick={confirm} disabled={sending || !!superseded}>Send</button><button type="button" onclick={cancel} disabled={sending}>Cancel</button></div>
  </div>{/if}
</section>

<style>
  .viewer { display: flex; flex-direction: column; height: 100%; min-height: 0; background: var(--bg); }
  /* A second bar under the viewer's own, so it stays slim. */
  header { position:relative; display:flex; align-items:center; gap:8px; height:40px; flex:none; min-width:0; padding:0 8px 0 16px; border-bottom:1px solid var(--line); }
  header strong { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:13px; font-weight:600; }
  button, a { font:inherit; font-size:13px; color:var(--muted); cursor:pointer; }
  button { border:0; border-radius:6px; background:none; padding:6px 8px; }
  button:hover, a:hover { background:var(--hover); color:var(--text); }
  .icon { display:grid; place-items:center; flex:none; width:32px; height:32px; padding:0; border-radius:6px; text-decoration:none; }
  .back { flex:none; white-space:nowrap; }
  .spacer { flex:1; }
  .anchor { position:relative; flex:none; }
  .version { color:var(--muted); font-size:12px; white-space:nowrap; }
  .version.active { background:var(--selected); color:var(--text); }
  button:focus-visible, a:focus-visible { outline:2px solid var(--focus); outline-offset:2px; }
  .popover { position:absolute; z-index:2; top:calc(100% + 12px); left:0; min-width:260px; padding:6px; background:var(--raised); border:1px solid var(--line); border-radius:8px; box-shadow:0 8px 24px var(--shadow-lg); }
  .versions button { display:flex; align-items:flex-start; gap:10px; width:100%; text-align:left; }
  .versions button[aria-current="true"] { background:var(--selected); }
  .pill { padding:2px 6px; border-radius:8px; color:var(--focus); background:var(--selected); font:600 11px var(--mono); }
  .version-meta { min-width:0; color:var(--text); }
  .version-meta small { display:block; max-width:240px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; color:var(--muted); }
  .older { padding: 8px 16px; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 12.5px; }
  .older button { color: var(--focus); }
  .stage { flex: 1; min-height: 0; display: flex; overflow: hidden; background: var(--bg); }
  iframe { display:block; width:100%; height:100%; border:0; color-scheme:inherit; }
  .failure { margin: auto; color: var(--muted); font-size: 13px; }
  .failure button { text-decoration: underline; }
  .confirm { flex: none; padding: 12px 16px; border-top: 1px solid var(--line); background: var(--raised); }
  .confirm p { margin: 0 0 8px; color: var(--muted); font-size: 12.5px; }
  .confirm strong { color: var(--text); font-weight: 600; }
  .confirm pre { margin: 0 0 8px; max-height: 120px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font: 13px var(--mono); }
  .confirm button { margin-right: 8px; border: 1px solid var(--firm); color: var(--text); }
  .confirm .send { border-color: var(--focus); background: var(--focus); color: var(--bg); font-weight: 600; }
  .confirm .send:hover:enabled { background: color-mix(in srgb, var(--focus) 88%, var(--text)); }
  .confirm button:disabled { opacity: .5; cursor: default; }
  @media (max-width:640px) { header { height:44px; } .icon { width:44px; height:44px; } .back, button.version, .versions button { min-height:44px; } .anchor { position:static; } .popover { top:44px; left:12px; right:12px; min-width:0; } }
</style>
