// A composer draft is kept in localStorage per conversation, so a reload keeps the text. Writes wait until typing pauses.
// Storage may be blocked or full. Then the draft stays in memory for this visit, and a save or send never fails.
export const draftWait = 500;
const prefix = "dyna.draft:";

type DraftStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;
type DraftTimers = { setTimeout(run: () => void, ms: number): unknown; clearTimeout(handle: unknown): void };

export class ComposerDrafts {
  private pending = new Map<string, string>();
  // This visit's text per conversation. It shadows storage, so a failed removal cannot bring back a sent draft.
  private visit = new Map<string, string>();
  private timer: unknown;
  // A getter, so that reading localStorage itself can fail inside the same guard as its methods.
  private storage: () => DraftStorage;
  private timers: DraftTimers;

  constructor(storage: () => DraftStorage, timers: DraftTimers = globalThis) {
    this.storage = storage;
    this.timers = timers;
  }

  load(conversation: string): string {
    const kept = this.visit.get(conversation);
    if (kept !== undefined) return kept;
    try {
      return this.storage().getItem(prefix + conversation) ?? "";
    } catch {
      return "";
    }
  }
  // The text the composer shows and send uses: what the owner typed this visit, else the saved draft.
  draft(conversation: string, typed: string | undefined): string {
    return typed ?? this.load(conversation);
  }
  save(conversation: string, text: string): void {
    this.pending.set(conversation, text);
    this.timers.clearTimeout(this.timer);
    this.timer = this.timers.setTimeout(() => this.flush(), draftWait);
  }
  // A sent draft drops its pending write, so a late timer cannot bring the text back.
  clear(conversation: string): void {
    this.pending.delete(conversation);
    this.visit.set(conversation, "");
    try {
      this.storage().removeItem(prefix + conversation);
    } catch {
      // Blocked storage keeps no draft to remove; the empty visit text hides any saved one.
    }
  }
  flush(): void {
    this.timers.clearTimeout(this.timer);
    for (const [conversation, text] of this.pending) {
      this.visit.set(conversation, text);
      try {
        this.storage().setItem(prefix + conversation, text);
      } catch {
        // Full or blocked storage keeps the text for this visit only.
      }
    }
    this.pending.clear();
  }
}
