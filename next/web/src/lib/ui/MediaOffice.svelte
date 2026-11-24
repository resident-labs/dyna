<script lang="ts">
  import MediaFile from "./MediaFile.svelte";
  import { fileSize } from "../feed.ts";
  import { theme } from "../theme.svelte.ts";
  import { officeSizeLimit, officeTheme, parseOfficeFrameMessage, readOfficeBytes } from "../office.ts";
  import type { Attachment } from "../contract.gen.ts";
  import type { OfficeKind } from "../media.ts";

  let { file, src, kind, onfolder, onclose }: { file: Attachment; src: string; kind: OfficeKind; onfolder?: () => void; onclose?: () => void } = $props();

  let frame = $state<HTMLIFrameElement>();
  let ready = $state(false);
  let rendered = $state(false);
  let status = $state<"loading" | "ready" | "too-large" | "load-failed" | "parse-failed">("loading");
  let attempt = $state(0);
  let renderId = 0;
  let pendingBuffer = $state.raw<ArrayBuffer | undefined>();
  let lastOpen = 0;

  $effect(() => {
    src; kind; attempt;
    if (file.bytes > officeSizeLimit) {
      status = "too-large";
      pendingBuffer = undefined;
      return;
    }
    const controller = new AbortController();
    status = "loading";
    rendered = false;
    pendingBuffer = undefined;
    void readOfficeBytes(src, controller.signal).then(result => {
      if (controller.signal.aborted) return;
      if (result === "too-large") { status = "too-large"; return; }
      pendingBuffer = result;
    }, () => {
      if (!controller.signal.aborted) status = "load-failed";
    });
    return () => controller.abort();
  });

  // Send the ArrayBuffer into the sandboxed iframe once both bytes and frame are ready.
  $effect(() => {
    const buffer = pendingBuffer;
    if (!ready || !buffer || !frame?.contentWindow) return;
    pendingBuffer = undefined;
    const id = ++renderId;
    frame.contentWindow.postMessage(
      { type: "office:render", id, kind, name: file.name, buffer, theme: officeTheme(theme()) },
      "*",
      [buffer],
    );
  });

  // Keep the sandboxed viewer in sync when the user switches themes while it is open.
  $effect(() => {
    const current = officeTheme(theme());
    if (!ready || !frame?.contentWindow) return;
    frame.contentWindow.postMessage({ type: "office:theme", theme: current }, "*");
  });

  let loaded = 0;

  function onFrameLoad() {
    loaded++;
    // A second load means the frame navigated away after its initial load; fail closed.
    if (loaded > 1) {
      ready = false;
      rendered = false;
      status = "parse-failed";
    }
  }

  function onMessage(event: MessageEvent<unknown>) {
    if (!frame || event.source !== frame.contentWindow || event.origin !== "null") return;
    const message = parseOfficeFrameMessage(event.data);
    if (!message) return;
    if (message.type === "office:ready") {
      ready = true;
      return;
    }
    if (message.type === "office:rendered" && message.id === renderId) {
      rendered = true;
      status = "ready";
      return;
    }
    if (message.type === "office:error" && message.id === renderId) {
      rendered = false;
      status = "parse-failed";
      return;
    }
    if (message.type === "office:close") {
      if (document.activeElement === frame) onclose?.();
      return;
    }
    if (message.type === "office:open") {
      if (document.activeElement !== frame || navigator.userActivation?.isActive !== true) return;
      const now = Date.now();
      if (now - lastOpen >= 1000) {
        lastOpen = now;
        window.open(message.url, "_blank", "noopener,noreferrer");
      }
    }
  }
</script>

<svelte:window onmessage={onMessage} />

{#if status === "too-large"}
  <MediaFile {file} {src} reason={`Files over ${fileSize(officeSizeLimit)} cannot be previewed.`} />
{:else if status === "load-failed"}
  <div class="state">The file did not load. <button type="button" onclick={() => attempt++}>Retry</button>{#if onfolder} · <button type="button" onclick={onfolder}>Show in folder</button>{/if}</div>
{:else if status === "parse-failed"}
  <MediaFile {file} {src} reason="Couldn't preview this file." />
{:else}
  <div class="host">
    {#if !rendered}<div class="loading">Loading…</div>{/if}
    <iframe
      bind:this={frame}
      class:visible={rendered}
      src="/office.html"
      title={file.name}
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      onload={onFrameLoad}
    ></iframe>
  </div>
{/if}

<style>
  .host { position: relative; width: 100%; height: 100%; min-height: 0; background: var(--sidebar); }
  iframe { display: block; width: 100%; height: 100%; border: 0; opacity: 0; }
  iframe.visible { opacity: 1; }
  .loading { position: absolute; inset: 0; display: grid; place-items: center; color: var(--muted); font-size: 13px; pointer-events: none; }
  .state { padding: 24px; color: var(--muted); font-size: 13px; }
  .state button { padding: 0; border: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
</style>
