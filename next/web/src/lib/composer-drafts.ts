// A composer draft is kept in localStorage per conversation, so a reload keeps the text. Writes wait until typing pauses.
export const draftWait = 500;
const prefix = "dyna.draft:";

type DraftStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;
type DraftTimers = { setTimeout(run: () => void, ms: number): unknown; clearTimeout(handle: unknown): void };

export class ComposerDrafts {
  private pending = new Map<string, string>();
  private timer: unknown;
  private storage: DraftStorage;
  private timers: DraftTimers;

  constructor(storage: DraftStorage, timers: DraftTimers = globalThis) {
    this.storage = storage;
    this.timers = timers;
  }

  load(conversation: string): string {
    return this.storage.getItem(prefix + conversation) ?? "";
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
    this.storage.removeItem(prefix + conversation);
  }
  flush(): void {
    this.timers.clearTimeout(this.timer);
    for (const [conversation, text] of this.pending) this.storage.setItem(prefix + conversation, text);
    this.pending.clear();
  }
}
