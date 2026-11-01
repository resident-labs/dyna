import type { Row } from "./feed.ts";
import type { Attachment, Principal } from "./contract.gen.ts";

export type MediaItem = { file: Attachment; key: string; from: Principal; at: string };

export function conversationMedia(rows: Row[]): MediaItem[] {
  return rows.flatMap(row => row.kind === "post"
    ? row.attachments.filter(file => !file.visual && /^(image|video)\//.test(file.media))
      .map(file => ({ file, key: row.key, from: row.from, at: row.at }))
    : []);
}
