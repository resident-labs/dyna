<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import StatusPill from "./StatusPill.svelte";
  import { formatTime, type Row } from "../feed.ts";
  let { row, author, root, onseen, children }: { row: Extract<Row, { kind: "failed" }>; author: string; root: boolean; onseen?: () => void; children?: Snippet } = $props();
  // The owner has seen a failed turn once its row stays on screen for a second while the page is visible, which
  // clears the session's mark. The wait skips rows that only pass by, such as while the feed jumps to its end on open.
  // Feed passes a new callback with every event, so the attachment reads it once; reading it as a dependency
  // would restart the wait on each event, and a busy room would never count the row as seen.
  function seen(node: HTMLElement) {
    const done = untrack(() => onseen);
    if (!done) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let shown = false;
    const start = () => {
      clearTimeout(timer);
      if (shown && document.visibilityState === "visible") timer = setTimeout(() => { done(); stop(); }, 1000);
    };
    const observer = new IntersectionObserver(([entry]) => { shown = entry.isIntersecting; start(); });
    const stop = () => { clearTimeout(timer); observer.disconnect(); document.removeEventListener("visibilitychange", start); };
    observer.observe(node);
    document.addEventListener("visibilitychange", start);
    return stop;
  }
</script>

<!-- As v1's turn result, a failed turn uses the post layout: the session's avatar, then its name line and the cause. -->
<article class="failed" {@attach seen}>
  <span class="mark"><StatusPill tone="failed" label={row.label} dot triangle /></span>
  <p class="name"><strong>{author}</strong><span class="tail">{row.label === "Turn failed" ? "’s turn failed" : ` ${row.label.toLowerCase()}`}{#if row.at}{" "}<time datetime={row.at}>{formatTime(row.at)}</time>{/if}</span></p>
  {#if row.cause}<p class="cause">{row.cause}{row.cause.endsWith(".") ? "" : "."}</p>{/if}
  {#if children}<div class="more">{@render children()}</div>{/if}
</article>

<style>
  .failed { display: grid; grid-template-columns: 32px minmax(0, 1fr); column-gap: 12px; padding: 6px 16px; }
  .mark { grid-row: 1 / span 2; display: flex; justify-content: center; padding-top: 2px; }
  p { grid-column: 2; margin: 0; }
  .name { overflow-wrap: anywhere; }
  .tail { white-space: nowrap; }
  .name time { margin-left: 4px; }
  .cause { color: var(--muted); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .more { grid-column: 2; margin-top: 8px; }
  .more:empty { display: none; }
  time { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
</style>
