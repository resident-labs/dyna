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
    Promise.all(turns.map(turn => call("transcript.read", { session: this.#session, turn }))).then(
      reads => this.#reads.set(key, { steps: reads.flatMap(read => read.steps ?? []), truncated: reads.some(read => read.truncated) }),
      () => this.#reads.set(key, "failed"),
    );
  }
}
