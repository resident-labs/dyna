import type { Attachment, Principal } from "./contract.gen.ts";

export type MediaItem = { file: Attachment; key: string; from: Principal; at: string };

export type MediaRow = { kind: "post"; key: string; from: Principal; at: string; attachments: Attachment[] } | { kind: Exclude<import("./feed.ts").Row["kind"], "post"> };

export function conversationMedia(rows: MediaRow[]): MediaItem[] {
  return rows.flatMap(row => row.kind === "post"
    ? row.attachments.filter(file => !file.visual && /^(image|video)\//.test(file.media))
      .map(file => ({ file, key: row.key, from: row.from, at: row.at }))
    : []);
}
