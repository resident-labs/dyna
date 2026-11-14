<script lang="ts">
  import type { Snippet } from "svelte";
  import { call } from "../client.ts";
  import type { TranscriptItem, TranscriptPart, TranscriptReadResponse, TranscriptTurn } from "../contract.gen.ts";
  import { watchTranscripts } from "../stream.ts";

  // One turn of an in-box agent, read from the session's transcript by the ID of the call that asked: its steps as they happen while
  // it runs, and once more when it ends. A call with no agent, such as an older one, shows nothing. The parts of a long step load whole on request.
  let { session, id, running, oncount, children }: {
    session: string; id: string; running: boolean; oncount: (steps: number) => void;
    children: Snippet<[turn: TranscriptTurn, chunks: Record<string, string>, load: (item: TranscriptItem, part: TranscriptPart) => Promise<void>]>;
  } = $props();
  let turn = $state<TranscriptTurn>();
  let chunks: Record<string, string> = $state({});
  let reading = false, again = false;
  async function read() {
    if (reading) { again = true; return; }
    reading = true;
    try {
      const page = await call("transcript.read", { session, agent: id });
      turn = page.turns?.[0];
      oncount(turn?.items?.filter(item => item.kind === "tool").length ?? 0);
    } catch { /* no such agent: nothing to show */ }
    finally { reading = false; if (again) { again = false; void read(); } }
  }
  async function load(item: TranscriptItem, part: TranscriptPart) {
    if (!turn) return;
    let offset: number | undefined = 0, value = "";
    do {
      const page: TranscriptReadResponse = await call("transcript.read", { session, agent: id, turn: turn.id, item: item.id, part, offset });
      value += page.chunk ?? "";
      offset = page.nextOffset;
    } while (offset !== undefined);
    chunks[item.id + "/" + part] = value;
  }
  // A running agent follows the session's transcript; an ended one reads once more, for its last steps.
  $effect(() => {
    void read();
    if (!running) return;
    return watchTranscripts(changed => { if (!changed || changed === session) void read(); });
  });
</script>

{#if turn?.items?.length}{@render children(turn, chunks, load)}{/if}
