<script lang="ts">
  import type { Snippet } from "svelte";
  import MediaText from "./MediaText.svelte";
  import MediaFile from "./MediaFile.svelte";
  import { mediaKind, mediaSrc, type MediaItem } from "../media.ts";
  import { filesURL, parentPath } from "../shared.ts";
  let { item, rendered, image, visual }: { item: MediaItem; rendered: boolean; image?: Snippet; visual?: Snippet } = $props();
  let src = $derived(mediaSrc(item)), kind = $derived(mediaKind(item.file));
  let folder = $derived(item.sharedPath !== undefined ? filesURL(item.room!, parentPath(item.sharedPath)) : undefined);
  let failed = $state(false), loading = $state(false);
  $effect(() => {
    failed = false; loading = false;
    // Text modes already check their Range response. Frames and download-only cards cannot
    // report HTTP failures, so check a byte before showing those shared-file modes.
    if (item.sharedPath === undefined || ["text", "markdown", "table"].includes(kind) || !rendered) return;
    const controller = new AbortController();
    loading = true;
    void fetch(src, { signal: controller.signal, headers: { Range: "bytes=0-0" } }).then(response => {
      if (!controller.signal.aborted) { failed = !response.ok && response.status !== 416; loading = false; }
      void response.body?.cancel();
    }, () => { if (!controller.signal.aborted) { failed = true; loading = false; } });
    return () => controller.abort();
  });
</script>
      {#if loading}<div class="error">Loading…</div>
      {:else if failed}<div class="error">The file did not load. {#if folder}<a href={folder}>Show in folder</a>{/if}</div>
      {:else if item.file.visual && rendered}
        {@render visual?.()}
      {:else if kind === "video"}
        {#key src}<video {src} controls preload="metadata" autoplay></video>{/key}
      {:else if kind === "image"}
        {#if image}{@render image()}{:else}<img {src} alt={item.file.name} onerror={() => failed = true} />{/if}
      {:else if (kind === "html" || kind === "svg") && rendered}
        <!-- As a visual card's frame, on the file route, which serves the file sandboxed. -->
        {#key src}
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <iframe class:drawing={kind === "svg"} tabindex={kind === "svg" ? -1 : undefined} {src} title={item.file.name} sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe>
        {/key}
      {:else if kind === "pdf" && navigator.pdfViewerEnabled}
        <!-- The browser's viewer refuses a sandbox attribute. The file route's own sandbox header still applies to anything that is not a PDF. -->
        {#key src}<iframe {src} title={item.file.name} referrerpolicy="no-referrer"></iframe>{/key}
      {:else if kind === "file" || kind === "pdf"}
        <MediaFile file={item.file} {src} reason="No preview for this file." />
      {:else}
        {#key src}<MediaText file={item.file} {src} {kind} {rendered} {folder} />{/key}
      {/if}
<style>
  img, video { display: block; max-width: 100%; max-height: 100%; object-fit: contain; }
  iframe { display: block; width: 100%; height: 100%; border: 0; }
  iframe.drawing { color-scheme: light; pointer-events: none; }
  .error { padding: 24px; color: var(--muted); font-size: 13px; }
  .error a { color: var(--focus); }
</style>
