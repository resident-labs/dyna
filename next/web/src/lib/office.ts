import type { OfficeKind } from "./media.ts";
import type { Theme, ThemeMode } from "./themes.ts";

export const officeSizeLimit = 25 << 20;

export type OfficeTheme = {
  mode: ThemeMode;
  bg: string;
  sidebar: string;
  raised: string;
  line: string;
  firm: string;
  selected: string;
  hover: string;
  focus: string;
  text: string;
  muted: string;
  shadow: string;
};

export function officeTheme(theme: Theme): OfficeTheme {
  return {
    mode: theme.mode,
    bg: theme.tokens["--bg"],
    sidebar: theme.tokens["--sidebar"],
    raised: theme.tokens["--raised"],
    line: theme.tokens["--line"],
    firm: theme.tokens["--firm"],
    selected: theme.tokens["--selected"],
    hover: theme.tokens["--hover"],
    focus: theme.tokens["--focus"],
    text: theme.tokens["--text"],
    muted: theme.tokens["--muted"],
    shadow: theme.tokens["--shadow-md"],
  };
}

export type OfficeParentMessage =
  | { type: "office:render"; id: number; kind: OfficeKind; name: string; buffer: ArrayBuffer; theme: OfficeTheme }
  | { type: "office:theme"; theme: OfficeTheme };

export type OfficeFrameMessage =
  | { type: "office:ready" }
  | { type: "office:rendered"; id: number }
  | { type: "office:error"; id: number }
  | { type: "office:open"; url: string }
  | { type: "office:close" };

export const validHttpUrl = (value: unknown): string | null => {
  if (typeof value !== "string" || value.length === 0 || value.length > 4096) return null;
  let parsed: URL;
  try { parsed = new URL(value); } catch { return null; }
  return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : null;
};

// Validate messages coming out of the sandboxed office iframe once at the parent boundary.
export function parseOfficeFrameMessage(data: unknown): OfficeFrameMessage | null {
  if (!data || typeof data !== "object") return null;
  const record = data as Record<string, unknown>;
  if (record.type === "office:ready") return { type: "office:ready" };
  if (record.type === "office:close") return { type: "office:close" };
  if ((record.type === "office:rendered" || record.type === "office:error") && typeof record.id === "number" && Number.isInteger(record.id)) {
    return { type: record.type, id: record.id };
  }
  if (record.type === "office:open") {
    const url = validHttpUrl(record.url);
    return url ? { type: "office:open", url } : null;
  }
  return null;
}

// Validate messages arriving inside the sandboxed office iframe from its parent.
export function parseOfficeParentMessage(data: unknown): OfficeParentMessage | null {
  if (!data || typeof data !== "object") return null;
  const record = data as Record<string, unknown>;
  const theme = parseTheme(record.theme);
  if (!theme) return null;
  if (record.type === "office:theme") return { type: "office:theme", theme };
  if (
    record.type === "office:render" &&
    typeof record.id === "number" && Number.isInteger(record.id) &&
    (record.kind === "word" || record.kind === "spreadsheet" || record.kind === "slides") &&
    typeof record.name === "string" &&
    record.buffer instanceof ArrayBuffer
  ) {
    return { type: "office:render", id: record.id, kind: record.kind, name: record.name, buffer: record.buffer, theme };
  }
  return null;
}

const themeKeys: readonly (keyof Omit<OfficeTheme, "mode">)[] = [
  "bg", "sidebar", "raised", "line", "firm", "selected", "hover", "focus", "text", "muted", "shadow",
];

function parseTheme(value: unknown): OfficeTheme | null {
  if (!value || typeof value !== "object") return null;
  const record = value as Record<string, unknown>;
  if (record.mode !== "dark" && record.mode !== "light") return null;
  const out = { mode: record.mode } as OfficeTheme;
  for (const key of themeKeys) {
    const item = record[key];
    if (typeof item !== "string" || item.length === 0 || item.length > 64) return null;
    out[key] = item;
  }
  return out;
}

// Read up to limit + 1 bytes so a file over the cap is detected even when Content-Length is missing.
export async function readOfficeBytes(src: string, signal: AbortSignal, limit = officeSizeLimit, load: typeof fetch = fetch): Promise<ArrayBuffer | "too-large"> {
  const response = await load(src, { signal });
  if (!response.ok) throw new Error("The file did not load");
  const declared = Number(response.headers.get("Content-Length") ?? "");
  if (Number.isSafeInteger(declared) && declared > limit) {
    void response.body?.cancel().catch(() => {});
    return "too-large";
  }
  const reader = response.body?.getReader();
  if (!reader) {
    const buffer = await response.arrayBuffer();
    return buffer.byteLength > limit ? "too-large" : buffer;
  }
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > limit) {
      void reader.cancel().catch(() => {});
      return "too-large";
    }
    chunks.push(value);
  }
  const merged = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return merged.buffer;
}
