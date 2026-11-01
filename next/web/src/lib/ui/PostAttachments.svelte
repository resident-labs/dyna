<script lang="ts">
  import VisualBanner from "./VisualBanner.svelte";
  import { fileSize, isImage, isThumb } from "../feed.ts";
  import type { Attachment, EventID, VisualCard } from "../contract.gen.ts";
  let { attachments, newest, onvisual, event, at, onmedia }: { attachments: Attachment[]; newest: Map<string, VisualCard>; onvisual: (visual: VisualCard) => void; event?: EventID; at?: string; onmedia: (file: Attachment) => void } = $props();
  // The engine serves each file sandboxed; the owner's cookie covers the request.
  const url = (file: Attachment) => `/v1/files/${encodeURIComponent(file.sha256)}`;
  // The type badge on a file card, such as "TXT", as in v1.
  const extension = (name: string) => /\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1].toUpperCase() ?? "FILE";
  // Images the engine refused. Each shows as a file card without a link, as its link would open only the refusal.
  let failed: string[] = $state([]);
  // Images already retried once; a second failure leaves the card without Retry.
  let retried: string[] = $state([]);
  const retry = (file: Attachment) => { retried = [...retried, file.sha256]; failed = failed.filter(sha => sha !== file.sha256); };
  let images = $derived(attachments.filter(file => !file.visual && isThumb(file, failed)));
  let videos = $derived(attachments.filter(file => !file.visual && file.media.startsWith("video/")));
  let others = $derived(attachments.filter(file => !file.visual && !isThumb(file, failed) && !file.media.startsWith("video/")));
  let durations: Record<string, string> = $state({});
</script>
{#each attachments as file, i (i)}
  {@const visual = file.visual}
  {#if visual && event && at}<div><VisualBanner {visual} newest={newest.get(visual.id) ?? visual} {event} {at} onopen={() => onvisual(visual)} /></div>{/if}
{/each}
  <!-- As in v1, images come first as thumbnails, then a card per other file. Each opens the file in a new tab. -->
  {#if images.length}<div class="images" class:many={images.length > 1}>{#each images as file, i (i)}
    <button class="thumb" onclick={() => onmedia(file)} title={`${file.name} · ${fileSize(file.bytes)}`}>
      <img src={url(file)} alt={file.name} loading="lazy" onerror={() => { failed = [...failed, file.sha256]; }} /></button>{/each}</div>{/if}
  {#each videos as file}<button class="video" onclick={() => onmedia(file)} aria-label={`Open ${file.name}`}><video src={url(file)} preload="metadata" muted onloadedmetadata={e => { const seconds = Math.floor(e.currentTarget.duration); if (Number.isFinite(seconds)) durations[file.sha256] = `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`; }}></video><span class="play">▶</span><span class="duration">{durations[file.sha256] ?? ""}</span></button>{/each}
  {#each others as file, i (i)}{@const dead = failed.includes(file.sha256)}
    <svelte:element this={dead ? "span" : "a"} class="file" class:dead href={dead ? undefined : url(file)} target={dead ? undefined : "_blank"} rel={dead ? undefined : "noopener"}
      title={dead ? `${file.name} did not load` : `Open ${file.name}`}>
      <span class="ext" aria-hidden="true">{extension(file.name)}</span>
      <span class="meta"><span class="file-name">{file.name}</span><span class="size">{dead ? "Did not load" : fileSize(file.bytes)}</span></span>
      {#if !dead}<span class="open" aria-hidden="true">↗</span>
      {:else if isImage(file) && !retried.includes(file.sha256)}<button type="button" class="reload" onclick={() => retry(file)}>Retry</button>{/if}</svelte:element>{/each}
<style>
  /* v1's sizes: one image up to 420x300, several as 200x150 crops; a 420 px file card with a 40 px type badge. */
  .video { position: relative; display: block; width: min(420px, 100%); margin: 6px 0; padding: 0; border: 0; border-radius: 8px; overflow: hidden; background: var(--sidebar); cursor: pointer; }
  .video video { display: block; width: 100%; max-height: 300px; object-fit: contain; }
  .play, .duration { position: absolute; color: var(--screen-text); background: var(--screen-chip); padding: 6px; border-radius: 6px; }
  .play { left: 50%; top: 50%; transform: translate(-50%, -50%); } .duration { bottom: 6px; right: 6px; }
  .images { display: flex; flex-wrap: wrap; gap: 6px; margin: 6px 0 2px; }
  .thumb { padding: 0; border: 0; cursor: pointer; display: block; max-width: min(420px, 100%); border-radius: 8px; overflow: hidden; background: var(--sidebar); line-height: 0; }
  .thumb img { display: block; max-width: 100%; max-height: 300px; object-fit: contain; }
  .many .thumb { width: 200px; height: 150px; }
  .many .thumb img { width: 100%; height: 100%; max-height: none; object-fit: cover; }
  .file { display: flex; align-items: center; gap: 12px; width: min(420px, 100%); margin: 6px 0 2px; padding: 10px 12px;
    background: var(--sidebar); border: 1px solid var(--line); border-radius: 8px; color: var(--text); text-decoration: none; }
  .file:hover { background: var(--hover); }
  /* A file that did not load is plain text: nothing to open. */
  .file.dead { cursor: default; }
  .file.dead .file-name { color: var(--text); }
  .reload { margin-left: auto; padding: 0; border: 0; background: none; color: var(--muted); font: inherit; font-size: 12px; cursor: pointer; }
  .reload:hover { color: var(--text); text-decoration: underline; }
  .ext { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border-radius: 6px; background: var(--raised);
    color: var(--muted); font: 600 10.5px var(--mono); }
  .meta { display: flex; flex-direction: column; min-width: 0; }
  .file-name { color: var(--focus); font-weight: 500; font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .size { color: var(--muted); font-size: 12px; }
  .open { margin-left: auto; color: var(--muted); font-size: 16px; }
  .file:hover .open { color: var(--text); }
  .thumb:hover, .thumb:focus-visible, .file:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
</style>
