import type { Attachment, Principal } from "./contract.gen.ts";

export type MediaItem = { file: Attachment; key: string; position: number; from: Principal; at: string; event?: string; room?: string };

export type MediaRow = { key: string; from: Principal; at: string; event?: string; room?: string; attachments: Attachment[] };

export function conversationMedia(rows: MediaRow[]): MediaItem[] {
  return rows.flatMap(row => row.attachments.flatMap((file, position) => file.visual || /^(image|video)\//.test(file.media)
    ? [{ file, key: row.key, position, from: row.from, at: row.at, event: row.event, room: row.room }] : []));
}
