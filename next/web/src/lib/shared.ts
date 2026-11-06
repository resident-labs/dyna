import type { MediaItem } from "./media.ts";

export type SharedEntry = { name: string; dir: boolean; size: number; modified: string };
export type MessageSource = Pick<MediaItem, "key" | "from" | "at">;
export const sharedURL = (room: string, path: string, download = false) => `/v1/rooms/${encodeURIComponent(room)}/shared/file?${new URLSearchParams({ path, ...(download ? { download: "1" } : {}) })}`;
export const filesURL = (room: string, path = "") => `/r/${encodeURIComponent(room)}/files/${path.split("/").map(encodeURIComponent).join("/")}`;
export const parentPath = (path: string) => path.split("/").slice(0, -1).join("/");

// The viewer already recognizes source extensions; only rendered formats need a declared MIME type.
export function sharedMedia(room: string, path: string, source: MessageSource, bytes = 0): MediaItem {
  const name = path.split("/").at(-1) ?? path;
  const extension = name.split(".").at(-1)?.toLowerCase() ?? "";
  const media = ({ png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", avif: "image/avif", bmp: "image/bmp", ico: "image/x-icon", svg: "image/svg+xml", html: "text/html", htm: "text/html", pdf: "application/pdf", mp4: "video/mp4", webm: "video/webm" } as Record<string, string>)[extension] ?? "application/octet-stream";
  return { ...source, room, position: 0, src: sharedURL(room, path), file: { name, media, bytes, sha256: "" }, sharedPath: path };
}

export function sharedPaths(text: string): { start: number; text: string; path: string }[] {
  return [...text.matchAll(/\/box\/shared\/[^\s`\]\[)\}>"']+/g)].flatMap(match => {
    const value = match[0].replace(/[.,:;]+$/, "");
    return value.length > 12 ? [{ start: match.index!, text: value, path: value.slice(12) }] : [];
  });
}
