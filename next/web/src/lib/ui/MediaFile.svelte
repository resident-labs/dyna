<script lang="ts">
  import Icon from "./Icon.svelte";
  import { fileSize } from "../feed.ts";
  import { fileBadge } from "../media.ts";
  import type { Attachment } from "../contract.gen.ts";
  // The viewer's last mode: what the file is, and the one thing to do with it.
  let { file, src, reason }: { file: Attachment; src: string; reason: string } = $props();
</script>

<div class="file">
  <span class="kind" aria-hidden="true">{fileBadge(file.name)}</span>
  <strong>{file.name}</strong>
  <span class="meta">{fileSize(file.bytes)}</span>
  <p>{reason}</p>
  <a href={src} download={file.name}><Icon name="download" />Download</a>
</div>

<style>
  .file { display: grid; justify-items: center; gap: 4px; max-width: min(360px, 100%); padding: 24px; text-align: center; }
  .kind { display: grid; place-items: center; width: 56px; height: 56px; margin-bottom: 8px; border-radius: 8px; background: var(--raised); color: var(--muted); font: 600 12px var(--mono); }
  strong { max-width: 100%; overflow-wrap: anywhere; font-size: 15px; font-weight: 600; }
  .meta, p { color: var(--muted); font-size: 12.5px; }
  p { margin: 8px 0; }
  a { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border: 1px solid var(--firm); border-radius: 6px; color: var(--text); font-size: 13px; text-decoration: none; }
  a:hover { background: var(--hover); }
  a:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
