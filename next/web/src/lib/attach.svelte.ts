import { upload } from "./client.ts";
import type { Attachment } from "./contract.gen.ts";

// The engine's limits: a post carries at most 16 files of at most 25 MiB each.
export const maxFiles = 16;
export const maxBytes = 25 << 20;

// A file in the composer: it uploads at once, and the post carries its Attachment.
// An image has a local preview URL, since the engine serves a file only once a post refers to it.
export type DraftFile = { id: number; file: File; preview?: string; state: "saving" | "ready" | "failed"; attachment?: Attachment };

// One conversation's draft files, as v1's attachmentDraft keeps them. An upload that ends after
// the owner moves to another conversation still lands in the draft it started in.
export class FileDraft {
  items: DraftFile[] = $state([]);
  private next = 0;
  private readonly room: string;
  constructor(room: string) { this.room = room; }
  get ready(): Attachment[] { return this.items.flatMap(item => item.attachment ? [item.attachment] : []); }
  // Send waits while a file saves or has failed.
  get busy(): boolean { return this.items.some(item => item.state !== "ready"); }

  // Adds the files that fit the limits, beside the reserved files a forwarded post carries; it returns one line
  // that names what was left out and why, or "".
  add(files: Iterable<File>, reserved = 0): string {
    const all = [...files], big = all.filter(file => file.size > maxBytes), small = all.filter(file => file.size <= maxBytes);
    const fit = small.slice(0, Math.max(0, maxFiles - reserved - this.items.length)), over = small.length - fit.length;
    for (const file of fit) {
      this.items.push({ id: this.next++, file, state: "saving", preview: /^image\/(png|jpeg|gif|webp)$/.test(file.type) ? URL.createObjectURL(file) : undefined });
      this.save(this.next - 1);
    }
    return [
      big.length === 1 ? `${big[0].name} is over 25 MB and was not added.` : big.length ? `${big.length} files are over 25 MB and were not added.` : "",
      over ? `A post can carry ${maxFiles} files; ${over} ${over === 1 ? "was" : "were"} not added.` : "",
    ].filter(Boolean).join(" ");
  }
  remove(id: number): void { this.release(this.items.filter(item => item.id === id)); this.items = this.items.filter(item => item.id !== id); }
  // The ready files leave the draft with the post.
  take(): Attachment[] { const ready = this.ready; this.release(this.items); this.items = []; return ready; }
  private release(items: DraftFile[]): void { for (const item of items) if (item.preview) URL.revokeObjectURL(item.preview); }

  save(id: number): void {
    const item = this.items.find(entry => entry.id === id);
    if (!item) return;
    item.state = "saving";
    upload(item.file, this.room).then(attachment => this.set(id, { state: "ready", attachment }), () => this.set(id, { state: "failed" }));
  }
  // A file removed while it saved stays removed.
  private set(id: number, change: Partial<DraftFile>): void {
    const item = this.items.find(entry => entry.id === id);
    if (item) Object.assign(item, change);
  }
}
