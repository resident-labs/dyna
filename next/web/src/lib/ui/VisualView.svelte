<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { cardMessage, checkLabel, visualReplyFailure, worstCheck, type VisualVersion } from "../visual.ts";
  import { call } from "../client.ts";
  import { decodeEvent } from "../event.ts";
  import type { VisualSuperseded } from "../contract.gen.ts";

  let { versions, selected, room, names, send, onclose }: {
    versions: VisualVersion[]; selected: number; room: string; names: Map<string, string>;
    send: (text: string, session: string, event: string) => Promise<void>; onclose: () => void;
  } = $props();
  let version = $state(untrack(() => selected));
  let extra = $state<VisualVersion[]>([]);
  let allVersions = $derived([...extra, ...versions]);
  let current = $derived(allVersions.find(item => item.visual.version === version) ?? allVersions[0]);
  let latest = $derived(allVersions[0]);
  let older = $derived(current.visual.version < latest.visual.version);
  let checks = $derived(current.visual.checks ?? []);
  let passed = $derived(checks.filter(check => check.status === "pass").length);
  let menu = $state<"versions" | "checks" | "">("");
  const widths: (360 | 768 | "fit")[] = [360, 768, "fit"];
  let width = $state<360 | 768 | "fit">("fit");
  let stageWidth = $state(0);
  let frame = $state<HTMLIFrameElement>();
  let panel: HTMLElement;
  onMount(() => panel.focus());
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
  let url = $derived(`/v1/files/visual/${encodeURIComponent(current.event.id)}`);
  let scale = $derived(width === "fit" ? 1 : Math.min(1, stageWidth / width));
  let recipient = $derived(current.event.from.session ? names.get(current.event.from.session) : undefined);

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
  function pick(value: number) { version = value; menu = ""; pending = null; superseded = undefined; openFailed = false; }
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
    await send(text, recipient, current.event.id).then(() => { pending = null; reply(true); }, error => {
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

<svelte:window onmessage={message} />
<section class="viewer" aria-label="Visual card" tabindex="-1" bind:this={panel} onkeydown={event => { if (event.key === "Escape") { if (menu) menu = ""; else onclose(); } }}>
  <header>
    <button class="back" type="button" onclick={onclose}>← Back</button>
    <strong title={current.visual.title}>{current.visual.title}</strong>
    <a href={url} target="_blank" rel="noopener noreferrer">Open in a tab ↗</a>
    <button class="close" type="button" onclick={onclose}>Close</button>
  </header>
  <div class="toolbar">
    <div class="anchor"><button type="button" aria-expanded={menu === "versions"} onclick={() => menu = menu === "versions" ? "" : "versions"}>v{version} ▾</button>
      {#if menu === "versions"}<div class="popover versions">{#each allVersions as item (item.event.id)}<button type="button" onclick={() => pick(item.visual.version)}>v{item.visual.version}</button>{/each}</div>{/if}
    </div>
    <div class="widths" aria-label="Preview width">{#each widths as size}<button type="button" aria-pressed={width === size} onclick={() => width = size}>{size === "fit" ? "Fit" : size}</button>{/each}</div>
    <div class="spacer"></div>
    <div class="anchor"><button type="button" aria-expanded={menu === "checks"} onclick={() => menu = menu === "checks" ? "" : "checks"}>{passed === checks.length ? `${passed} checks passed` : `${passed} of ${checks.length} checks`} ▾</button>
      {#if menu === "checks"}<div class="popover checks"><strong>Checks</strong>
        {#each checks as check}<div class="check"><span aria-hidden="true">{check.status === "pass" ? "✓" : check.status === "skip" ? "–" : "!"}</span><div>{checkLabel(check)}<span class="sr-only">: {check.status === "pass" ? "Passed" : check.status === "skip" ? "Not checked" : check.status === "fail" ? "Failed" : "Needs review"}</span>
          {#if check.status === "skip"}<p class="detail">{check.name === "network" && !current.visual.allowNet ? "Not checked: the card asks for no network" : "Not checked"}</p>
          {:else if check.status === "warn" || check.status === "fail"}<p class="detail">{worstCheck([check])}</p>{/if}</div></div>
          {#if check.name === "widths" && check.status !== "pass"}{#each current.visual.shots ?? [] as shot}<img class="shot" src={"/v1/files/" + encodeURIComponent(shot.file)} alt={`Width check at ${shot.width} pixels`} />{/each}{/if}
        {/each}
      </div>{/if}
    </div>
  </div>
  {#if superseded}<div class="older" role="alert">This card has a newer version. <button type="button" onclick={openNew}>Open v{superseded.version}</button>{#if openFailed} The version did not open. Try again.{/if}</div>
  {:else if older}<div class="older">A newer loaded version is available. <button type="button" onclick={() => pick(latest.visual.version)}>Show v{latest.visual.version}</button></div>{/if}
  <div class="stage" bind:clientWidth={stageWidth}>
    {#if failed}<div class="failure">The card did not load. <button type="button" onclick={() => attempt++}>Retry</button></div>
    {:else if navigated}<div class="failure">The card tried to navigate away. Close it to stop the card.</div>
    {:else if !loading}
      {#key current.event.id + ":" + attempt}
        <div class="frame-wrap" style:width={width === "fit" ? "100%" : `${width * scale}px`} style:height={width === "fit" ? "100%" : `${current.visual.height * scale}px`}>
          <iframe bind:this={frame} src={url} title={current.visual.title} sandbox="allow-scripts" referrerpolicy="no-referrer"
            style:width={width === "fit" ? "100%" : `${width}px`} style:height={width === "fit" ? "100%" : `${current.visual.height}px`}
            style:transform={`scale(${scale})`} onload={frameLoaded}></iframe>
        </div>
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
  header { display: flex; align-items: center; gap: 12px; min-height: 52px; padding: 8px 16px; border-bottom: 1px solid var(--line); }
  header strong { flex: 1; min-width: 0; font-size: 16px; line-height: 1.25; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  button, a { font: inherit; font-size: 13px; color: var(--muted); cursor: pointer; }
  button { border: 0; border-radius: 6px; background: none; padding: 6px 8px; }
  button:hover { background: var(--hover); color: var(--text); }
  a { white-space: nowrap; }
  .back { display: none; }
  .toolbar { display: flex; align-items: center; gap: 8px; padding: 8px 16px; border-bottom: 1px solid var(--line); }
  .toolbar button { border: 1px solid var(--line); }
  .widths { display: flex; }
  .widths button[aria-pressed="true"] { background: var(--selected); color: var(--text); }
  .spacer { flex: 1; }
  .anchor { position: relative; }
  .popover { position: absolute; z-index: 2; top: calc(100% + 6px); padding: 8px; background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 8px 24px var(--shadow-lg); }
  .versions { left: 0; min-width: 180px; }
  .versions button { display: block; width: 100%; text-align: left; border: 0; }
  .checks { right: 0; width: min(360px, 85vw); color: var(--muted); font-size: 12.5px; }
  .checks strong { display: block; color: var(--text); padding: 4px 8px; }
  .check { display: grid; grid-template-columns: 16px 1fr; gap: 8px; padding: 6px 8px; }
  .detail { margin: 2px 0 0; color: var(--muted); }
  .sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .shot { display: block; max-width: 100%; max-height: 200px; object-fit: contain; }
  .older { padding: 8px 16px; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 12.5px; }
  .older button { color: var(--focus); }
  .stage { flex: 1; min-height: 0; display: flex; justify-content: center; overflow: auto; background: var(--sidebar); }
  .frame-wrap { flex: none; max-width: 100%; overflow: hidden; }
  iframe { display: block; border: 0; transform-origin: top left; }
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
  @media (max-width: 640px) {
    header strong { white-space: nowrap; display: block; text-overflow: ellipsis; }
    .back { display: block; white-space: nowrap; }
    .close, .widths { display: none; }
    header a { font-size: 0; } header a::after { content: "↗"; font-size: 16px; }
    .viewer button { min-height: 40px; }
  }
</style>
