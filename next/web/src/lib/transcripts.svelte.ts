import { SvelteMap } from "svelte/reactivity";
import { call } from "./client.ts";
import { rowTurns } from "./steps.ts";
import type { Event, TranscriptStep } from "./contract.gen.ts";

export type TurnRead = { steps: TranscriptStep[]; truncated: boolean } | "loading" | "failed";

// The saved steps of one session's work rows. A row reads its turns when it first opens, and a failed read waits for Retry.
export class TurnTranscripts {
  readonly #reads = new SvelteMap<string, TurnRead>();
  readonly #session: string;
  constructor(session: string) { this.#session = session; }

  of = (events: Event[]): TurnRead | undefined => this.#reads.get(rowTurns(events).key);

  read(events: Event[], again = false) {
    const { turns, key } = rowTurns(events);
    if (!turns.length || (this.#reads.has(key) && !again)) return;
    this.#reads.set(key, "loading");
    return Promise.all(turns.map(async turn => {
      const steps: TranscriptStep[] = [];
      let itemIndex = 0;
      let older = false;
      let tail = false;
      let postIndex = 0;
      let afterItem: string | undefined;
      let truncated = false;
      while (steps.length < 200) {
        const read = await call("transcript.read", { session: this.#session, turn, afterItem, tail, postIndex, ...(older && !tail && !afterItem ? { page: "older_items" as const, cursor: itemIndex } : {}) });
        steps.push(...(read.steps ?? []));
        const page = read.turns?.[0];
        if (!page?.more) break;
        if (steps.length >= 200) { truncated = true; break; }
        if (page.after) { afterItem = page.after; continue; }
        afterItem = undefined;
        if (page.tail) {
          const nextPost = page.nextPost ?? 0;
          if (tail && nextPost <= postIndex) { truncated = true; break; }
          tail = true;
          postIndex = nextPost;
          continue;
        }
        if (page.nextItem == null || (older && page.nextItem <= itemIndex)) { truncated = true; break; }
        itemIndex = page.nextItem;
        older = true;
      }
      if (steps.length > 200) { steps.length = 200; truncated = true; }
      return { steps, truncated };
    })).then(
      reads => this.#reads.set(key, { steps: reads.flatMap(read => read.steps ?? []), truncated: reads.some(read => read.truncated) }),
      () => this.#reads.set(key, "failed"),
    );
  }
}
