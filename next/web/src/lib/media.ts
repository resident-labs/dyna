// mediaKind and parseTable are ported from T3 Code (packages/shared/src/filePreview.ts and delimitedPreview.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import type { Attachment, Principal, TranscriptImage } from "./contract.gen.ts";
import { fileSize } from "./feed.ts";

// Every attachment is media. src is set when the bytes are not a room file, as for a read's image.
export type MediaItem = { file: Attachment; src?: string; key: string; position: number; from: Principal; at: string; event?: string; room?: string };

export type MediaRow = { key: string; from: Principal; at: string; event?: string; room?: string; src?: string; attachments: Attachment[] };

export type MediaKind = "visual" | "image" | "video" | "svg" | "html" | "pdf" | "markdown" | "table" | "text" | "file";

export function conversationMedia(rows: MediaRow[]): MediaItem[] {
  return rows.flatMap(row => row.attachments.map((file, position) => ({ file, src: row.src, key: row.key, position, from: row.from, at: row.at, event: row.event, room: row.room })));
}

export const mediaSrc = (item: Pick<MediaItem, "file" | "src">) => item.src ?? `/v1/files/${encodeURIComponent(item.file.sha256)}`;

// A read's image as a media row. It stays in the transcript, so no box has to exist for it to show.
export function readMedia(session: string, turn: string, at: string, path: string, image: TranscriptImage): MediaRow & { src: string } {
  const query = new URLSearchParams({ session, turn, item: image.item });
  return {
    key: image.item, from: { kind: "session", session }, at, src: `/v1/files/transcript?${query}`,
    attachments: [{ name: path.split("/").at(-1) || "Image", media: image.media, bytes: image.bytes, sha256: "" }],
  };
}

const mediaType = (file: Pick<Attachment, "media">) => file.media.split(";", 1)[0].trim().toLowerCase();
const extensionOf = (name: string) => /\.[^./\\]+$/.exec(name.toLowerCase())?.[0] ?? "";
// Types that say nothing about the content. .ts files are MPEG video to the system's type table.
const isGeneric = (media: string) => !media || media === "text/plain" || media === "application/octet-stream" || media === "video/mp2t";

export function tableDelimiter(file: Pick<Attachment, "name" | "media">): "," | "\t" | undefined {
  const media = mediaType(file);
  if (media === "text/csv") return ",";
  if (media === "text/tab-separated-values") return "\t";
  if (!isGeneric(media)) return undefined;
  const extension = extensionOf(file.name);
  return extension === ".csv" ? "," : extension === ".tsv" ? "\t" : undefined;
}

const textExtension = /^\.(txt|log|json|jsonc|jsonl|ndjson|yaml|yml|toml|ini|conf|config|env|xml|html|htm|svg|css|scss|sass|less|js|jsx|mjs|cjs|ts|tsx|mts|cts|py|pyi|rb|go|rs|swift|kt|kts|java|c|h|cc|cpp|hpp|cs|php|sh|bash|zsh|fish|sql|graphql|gql|vue|svelte|r|lua|ex|exs|erl|hs|clj|dart|diff|patch|lock|properties|gradle)$/;
const textName = /^(dockerfile|makefile|gemfile|rakefile|license|readme)(\.|$)/;
const structuredMedia = /^application\/(json|.*\+json|x-ndjson|xml|.*\+xml|javascript|x-javascript|yaml|x-yaml|toml|sql|x-sh)$/;

// The one place that decides how a file shows. The file route serves the declared type, so a page or drawing declared
// as anything else reads as its source.
export function mediaKind(file: Pick<Attachment, "name" | "media" | "visual">): MediaKind {
  if (file.visual) return "visual";
  const media = mediaType(file), name = file.name.toLowerCase(), extension = extensionOf(name), generic = isGeneric(media);
  if (media === "image/svg+xml") return "svg";
  if (media.startsWith("image/")) return "image";
  if (media.startsWith("video/") && !generic) return "video";
  if (media === "text/html") return "html";
  if (media === "application/pdf") return "pdf";
  if (media === "text/markdown" || media === "text/x-markdown" || generic && /^\.(md|markdown|mdown|mkd|mdx)$/.test(extension)) return "markdown";
  if (tableDelimiter(file)) return "table";
  if (media.startsWith("text/") || structuredMedia.test(media) || generic && (textExtension.test(extension) || textName.test(name))) return "text";
  return "file";
}

// The kinds with a rendered view and a source view. What the toggle offers next, in the owner's words.
const sourceWords: Partial<Record<MediaKind, string>> = { markdown: "rendered markdown", table: "table", html: "rendered page", svg: "rendered image", visual: "card" };
export const hasSource = (kind: MediaKind) => kind in sourceWords;
export const sourceToggle = (kind: MediaKind, rendered: boolean) => rendered ? "Show source" : `Show ${sourceWords[kind]}`;

// The badge on a file card, such as "TXT", as in v1.
export const fileBadge = (name: string) => /\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1].toUpperCase() ?? "FILE";

export const previewLimit = 1 << 20;
export type Preview = { text: string; truncated: boolean };

// null means the bytes are not text. A cut can split a character, so a truncated decode stops before it.
export function decodePreview(bytes: Uint8Array, limit = previewLimit): Preview | null {
  const kept = bytes.subarray(0, limit);
  if (kept.includes(0)) return null;
  const truncated = bytes.length > limit;
  return { text: new TextDecoder().decode(kept, { stream: truncated }), truncated };
}

// Asks for one byte past the limit, so a file exactly at the limit is not called truncated. A server may ignore the
// range, so the read also stops itself.
export async function readPreview(src: string, signal: AbortSignal, limit = previewLimit, load: typeof fetch = fetch): Promise<Preview | null> {
  const response = await load(src, { signal, headers: { Range: `bytes=0-${limit}` } });
  if (!response.ok) throw new Error("The file did not load");
  const reader = response.body?.getReader();
  const bytes = new Uint8Array(limit + 1);
  let size = 0;
  while (reader && size <= limit) {
    const { done, value } = await reader.read();
    if (done) break;
    const part = value.subarray(0, limit + 1 - size);
    bytes.set(part, size);
    size += part.length;
  }
  void reader?.cancel().catch(() => {});
  return decodePreview(bytes.subarray(0, size), limit);
}

export type Table = { rows: string[][]; width: number; moreRows: boolean; clipped: boolean };
export const tableLimits = { maxRows: 1000, maxColumns: 50, maxCellLength: 2000 };

// Quoted cells may hold delimiters and line breaks. partial says the text was cut, so its last row is not whole.
export function parseTable(text: string, delimiter: "," | "\t", partial = false, limits = tableLimits): Table {
  const rows: string[][] = [];
  let row: string[] = [], cell = "", quoted = false, clipped = false, moreRows = false;
  let rowStart = text.charCodeAt(0) === 0xfeff ? 1 : 0;
  const endCell = () => {
    if (cell.length > limits.maxCellLength) { cell = cell.slice(0, limits.maxCellLength); clipped = true; }
    if (row.length < limits.maxColumns) row.push(cell);
    else clipped = true;
    cell = "";
  };
  const done = () => ({ rows, width: rows.reduce((width, line) => Math.max(width, line.length), 0), moreRows, clipped });
  for (let index = rowStart; index < text.length; index++) {
    const char = text[index];
    if (char === '"') {
      if (quoted && text[index + 1] === '"') { cell += '"'; index++; continue; }
      if (quoted || cell === "") { quoted = !quoted; continue; }
    }
    if (!quoted && (char === delimiter || char === "\n" || char === "\r")) {
      endCell();
      if (char !== delimiter) {
        rows.push(row);
        row = [];
        if (char === "\r" && text[index + 1] === "\n") index++;
        rowStart = index + 1;
        if (rows.length === limits.maxRows) { moreRows = partial || index < text.length - 1; return done(); }
      }
    } else if (cell.length <= limits.maxCellLength) cell += char;
    else clipped = true;
  }
  if (rowStart < text.length && !partial) { endCell(); rows.push(row); }
  return done();
}

// The line above a preview that is not the whole file.
export function previewNote(file: Pick<Attachment, "bytes">, preview: Preview, table?: Table): string {
  const count = table && table.rows.length ? `Showing the first ${table.rows.length.toLocaleString("en-US")} rows` : "";
  const cut = preview.truncated ? `the first ${fileSize(previewLimit)} of ${fileSize(file.bytes)}` : "";
  if (table?.moreRows || preview.truncated) {
    if (count && cut) return `${count}, from ${cut}. Download the file for the rest.`;
    return `${count || `Showing ${cut}`}. Download the file for the rest.`;
  }
  return table?.clipped ? "Some long cells and wide rows are cut. Download the file for the rest." : "";
}

// Which way an arrow key moves through the media. A scroller that scrolls sideways keeps its arrows.
export function mediaStep(key: string, count: number, sideways: boolean): number {
  if (count < 2 || sideways) return 0;
  return key === "ArrowLeft" ? -1 : key === "ArrowRight" ? 1 : 0;
}
