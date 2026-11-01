<script lang="ts" module>
  import { SvelteMap } from "svelte/reactivity";
  import type { PreviewFit } from "../visual-preview.ts";
  // Rows recycle while the feed scrolls, so each capture is measured once and returns at its final height.
  const measured = new SvelteMap<string, PreviewFit>();
</script>

<script lang="ts">
  import type { EventID, VisualCard } from "../contract.gen.ts";
  import { fromNow } from "../feed.ts";
  import { theme } from "../theme.svelte.ts";
  import { acquirePreview, contentRows, declaredFit, previewFit } from "../visual-preview.ts";
  let { visual, newest, event, at, onopen }: { visual: VisualCard; newest: VisualCard; event: EventID; at: string; onopen: () => void } = $props();
  let older = $derived(visual.version < newest.version);
  let failed = $state(false), ready = $state(false), visible = $state(false), mounted = $state(false), width = $state(0);
  $effect(() => { visual.banner; visual.thumbnail; failed = false; ready = false; });
  let fit = $derived((!failed && measured.get(visual.banner)) || declaredFit(visual.height));
  // A quarter of the capture's size finds where its content ends and is cheap to read back. A canvas that refuses the read
  // leaves the card at its declared height.
  function measure(image: HTMLImageElement, banner: string) {
    if (measured.has(banner)) return;
    const canvas = document.createElement("canvas"); canvas.width = 240; canvas.height = 90;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) return;
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    try { measured.set(banner, previewFit(contentRows(context.getImageData(0, 0, canvas.width, canvas.height).data, canvas.width, canvas.height), canvas.height)); }
    catch { /* Unreadable: nothing to measure. */ }
  }
  let releasePreview: (() => void) | undefined;
  $effect(() => {
    if (!older && visible && (!visual.banner || failed)) {
      releasePreview = acquirePreview(() => { mounted = true; });
      return () => { releasePreview?.(); releasePreview = undefined; mounted = false; };
    }
  });
  function observe(node: HTMLElement) {
    const observer = new IntersectionObserver(entries => { visible = entries[0].isIntersecting; });
    observer.observe(node);
    return { destroy() { observer.disconnect(); visible = false; } };
  }
</script>

<button type="button" class="visual" class:older onclick={onopen}>
  {#if older}
    <span class="mini">{#if visual.thumbnail && !failed}<img src={"/v1/files/" + encodeURIComponent(visual.thumbnail)} alt="" loading="lazy" onerror={() => failed = true} />{/if}</span>
    <span class="say"><strong>{visual.title}</strong> · v{visual.version} · Superseded by v{newest.version}</span>
  {:else}
    <span class="art" class:ready class:clipped={fit.clipped} style:--fit={fit.height} use:observe bind:clientWidth={width}>
      {#if visual.banner && !failed}<img src={"/v1/files/" + encodeURIComponent(visual.banner)} alt="" loading="lazy" onload={event => { ready = true; if (event.currentTarget instanceof HTMLImageElement) measure(event.currentTarget, visual.banner); }} onerror={() => failed = true} />
      {:else if mounted}<iframe scrolling="no" src={"/v1/files/visual/" + encodeURIComponent(event) + "?theme=" + theme().mode} title="" sandbox="allow-scripts" tabindex="-1" aria-hidden="true" style:transform={"scale(" + width / 960 + ")"} onload={() => { ready = true; releasePreview?.(); }}></iframe>{/if}
    </span>
    <span class="row"><strong>{visual.title}</strong><span class="pill">v{visual.version}</span><time datetime={at}>updated {fromNow(at, Date.now())}</time></span>
  {/if}
</button>

<style>
  .visual { display:block; width:min(560px, 100%); margin:6px 0 2px; padding:0; text-align:left; border:1px solid var(--line); border-radius:12px; background:var(--sidebar); color:var(--text); font:inherit; overflow:hidden; cursor:pointer; }
  .visual:hover { border-color:var(--firm); }
  .visual:focus-visible { outline:2px solid var(--focus); outline-offset:2px; }
  /* The 960 by 360 capture, cut to where its content ends. */
  .art { position:relative; display:block; aspect-ratio:960 / var(--fit, 360); overflow:hidden; background:linear-gradient(90deg,var(--raised),var(--hover),var(--raised)) 0 0 / 200% 100%; animation:shimmer 1.4s ease-in-out infinite; }
  .art.ready { animation:none; background:var(--raised); }
  /* The fade says the page runs on, so a page that ends inside the preview has a hairline instead. */
  .art.clipped::after { content:""; position:absolute; inset:auto 0 0; height:28%; background:linear-gradient(transparent,var(--sidebar)); pointer-events:none; }
  .art:not(.clipped) { border-bottom:1px solid var(--line); }
  .art img { width:100%; height:100%; object-fit:cover; object-position:top; opacity:0; transition:opacity 200ms; }
  .art.ready img { opacity:1; }
  iframe { position:absolute; top:0; left:0; width:980px; height:380px; border:0; transform-origin:top left; pointer-events:none; color-scheme:inherit; }
  .row { display:flex; align-items:center; min-width:0; gap:8px; padding:10px 14px 12px; }
  .row strong { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:14.5px; font-weight:600; }
  .pill { flex:none; font:600 11px var(--mono); color:var(--focus); background:var(--selected); padding:1px 7px; border-radius:9px; }
  time { flex:none; margin-left:auto; color:var(--muted); font-size:12px; white-space:nowrap; }
  .older { display:inline-flex; align-items:center; gap:10px; width:auto; max-width:100%; padding:5px 12px 5px 5px; border-radius:8px; font-size:13px; color:var(--muted); }
  .mini { flex:none; width:48px; height:36px; overflow:hidden; border-radius:5px; background:var(--raised); }
  .mini img { width:100%; height:100%; object-fit:cover; }
  .say { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .say strong { color:var(--text); }
  @keyframes shimmer { from { background-position:100% 0; } to { background-position:-100% 0; } }
  @media (prefers-reduced-motion:reduce) { .art { animation:none; } }
</style>
