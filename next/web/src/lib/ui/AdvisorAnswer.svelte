<script lang="ts">
  import type { TranscriptItem, TranscriptPart } from "../contract.gen.ts";
  import { advisorReply } from "../steps.ts";
  import { hue } from "../identity.ts";
  import { formatTime } from "../feed.ts";
  import Avatar from "./Avatar.svelte";
  import Markdown from "./Markdown.svelte";

  // The advisor's answer to root's call, as a message in root's DM after the call's row, in the style of a child's message. It is
  // drawn from the call's result, so it is no post: the feed has nothing of it and it wakes nobody. A call a stop or a timeout ended
  // shows what the advisor had said, with the note of what ended it. A result cut for the transcript loads whole.
  let { item, chunks, session, onload }: {
    item: TranscriptItem; chunks: Record<string, string>; session: string;
    onload: (item: TranscriptItem, part: TranscriptPart) => Promise<void>;
  } = $props();
  const whole = $derived(chunks[item.id + "/output"]);
  $effect(() => { if (item.more && whole === undefined) void onload(item, "output").catch(() => {}); });
  const reply = $derived(advisorReply(whole ?? item.output ?? ""));
  const name = $derived(reply.advisor ? `advisor (${reply.advisor})` : "advisor");
  const note = $derived(reply.note ? reply.note.charAt(0).toUpperCase() + reply.note.slice(1) : "");
</script>

<div class="advice" id={"advice-" + item.id}>
  <div class="sender"><Avatar kind="session" name="advisor" size={20} /><strong style:--hue={hue("advisor")}>{name}</strong><time datetime={item.ended ?? item.at}>{formatTime(item.ended ?? item.at)}</time></div>
  {#if reply.answer}<Markdown rhythm="thread" text={reply.answer} source={{ key: item.id, from: { kind: "session", session }, at: item.at }} />{/if}
  {#if note}<p class="note">{note}</p>{/if}
</div>

<style>
  /* A child's message: the raised bubble from the left, mirroring the owner's. */
  .advice { align-self: flex-start; max-width: 80%; min-width: 0; padding: 12px 16px; background: var(--raised); border-radius: 12px; scroll-margin: 24px; }
  .sender { display: flex; align-items: center; gap: 8px; min-width: 0; margin-bottom: 4px; }
  strong { min-width: 0; overflow: hidden; color: oklch(var(--id-name) var(--hue)); font-size: 14px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  time { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .note { margin: 6px 0 0; color: var(--muted); font-size: 13px; }
</style>
