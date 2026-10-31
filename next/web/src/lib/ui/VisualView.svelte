<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { cardMessage, checkLabel, worstCheck, type VisualVersion } from "../visual.ts";

  let { versions, selected, onclose }: {
    versions: VisualVersion[]; selected: number; onclose: () => void;
  } = $props();
  let version = $state(untrack(() => selected));
  let current = $derived(versions.find(item => item.visual.version === version) ?? versions[0]);
  let latest = $derived(versions[0]);
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
  let loading = $state(true);
  let failed = $state(false);
  let navigated = $state(false);
  let attempt = $state(0);
  let loaded = 0;
  let lastOpen = 0;
  let url = $derived(`/v1/files/visual/${encodeURIComponent(current.event.id)}`);
  let scale = $derived(width === "fit" ? 1 : Math.min(1, stageWidth / width));

  $effect(() => {
    const source = url;
    attempt;
    const controller = new AbortController();
    loading = true; failed = false; navigated = false; loaded = 0;
    void fetch(source, { signal: controller.signal }).then(response => {
      if (!response.ok) failed = true;
      void response.body?.cancel();
      loading = false;
    }, () => { if (!controller.signal.aborted) { failed = true; loading = false; } });
    return () => controller.abort();
  });
  function pick(value: number) { version = value; menu = ""; }
  function message(event: MessageEvent<unknown>) {
    if (!frame || event.source !== frame.contentWindow || event.origin !== "null") return;
    const data = cardMessage(event.data);
    if (!data || document.activeElement !== frame || navigator.userActivation?.isActive !== true) return;
    const now = Date.now();
    if (now - lastOpen >= 2000) { lastOpen = now; window.open(data.url, "_blank", "noopener,noreferrer"); }
  }
  function frameLoaded() {
    loaded++;
    // A second load is navigation initiated by the card. Stop it rather than displaying another document.
    if (loaded > 1) navigated = true;
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
      {#if menu === "versions"}<div class="popover versions">{#each versions as item (item.event.id)}<button type="button" onclick={() => pick(item.visual.version)}>v{item.visual.version}</button>{/each}</div>{/if}
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
  {#if older}<div class="older">A newer loaded version is available. <button type="button" onclick={() => pick(latest.visual.version)}>Show v{latest.visual.version}</button></div>{/if}
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
  @media (max-width: 640px) {
    header strong { white-space: nowrap; display: block; text-overflow: ellipsis; }
    .back { display: block; white-space: nowrap; }
    .close, .widths { display: none; }
    header a { font-size: 0; } header a::after { content: "↗"; font-size: 16px; }
    .viewer button { min-height: 40px; }
  }
</style>
