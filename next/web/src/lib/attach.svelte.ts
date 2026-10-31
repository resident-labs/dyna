import { upload } from "./client.ts";
import type { Attachment } from "./contract.gen.ts";

// The engine's limits: a post carries at most 16 files of at most 25 MiB each.
export const maxFiles = 16;
export const maxBytes = 25 << 20;
export const limits = "A post can carry 16 files of up to 25 MB each.";

// A file in the composer: it uploads at once, and the post carries its Attachment.
// An image has a local preview URL, since the engine serves a file only once a post refers to it.
export type DraftFile = { id: number; file: File; preview?: string; state: "saving" | "ready" | "failed"; attachment?: Attachment };

// One conversation's draft files, as v1's attachmentDraft keeps them. An upload that ends after
// the owner moves to another conversation still lands in the draft it started in.
export class FileDraft {
  items: DraftFile[] = $state([]);
  private next = 0;
  get ready(): Attachment[] { return this.items.flatMap(item => item.attachment ? [item.attachment] : []); }
  // Send waits while a file saves or has failed.
  get busy(): boolean { return this.items.some(item => item.state !== "ready"); }

  // Adds the files that fit the limits; it returns the limits line when any did not fit.
  add(files: Iterable<File>): string {
    const all = [...files], fit = all.filter(file => file.size <= maxBytes).slice(0, Math.max(0, maxFiles - this.items.length));
    for (const file of fit) {
      this.items.push({ id: this.next++, file, state: "saving", preview: /^image\/(png|jpeg|gif|webp)$/.test(file.type) ? URL.createObjectURL(file) : undefined });
      this.save(this.next - 1);
    }
    return fit.length < all.length ? limits : "";
  }
  remove(id: number): void { this.release(this.items.filter(item => item.id === id)); this.items = this.items.filter(item => item.id !== id); }
  // The ready files leave the draft with the post.
  take(): Attachment[] { const ready = this.ready; this.release(this.items); this.items = []; return ready; }
  private release(items: DraftFile[]): void { for (const item of items) if (item.preview) URL.revokeObjectURL(item.preview); }

  save(id: number): void {
    const item = this.items.find(entry => entry.id === id);
    if (!item) return;
    item.state = "saving";
    upload(item.file).then(attachment => this.set(id, { state: "ready", attachment }), () => this.set(id, { state: "failed" }));
  }
  // A file removed while it saved stays removed.
  private set(id: number, change: Partial<DraftFile>): void {
    const item = this.items.find(entry => entry.id === id);
    if (item) Object.assign(item, change);
  }
}
