import type { JSONData } from "./contract.gen.ts";

export type DataNode = {
  source: string;
  start: number;
  end: number;
  kind: "object" | "array" | "string" | "number" | "boolean" | "null";
};

function node(source: string, start: number, end: number): DataNode {
  const first = source[start];
  const kind =
    first === "{"
      ? "object"
      : first === "["
        ? "array"
        : first === '"'
          ? "string"
          : first === "n"
            ? "null"
            : first === "t" || first === "f"
              ? "boolean"
              : "number";
  return { source, start, end, kind };
}
function stringEnd(source: string, start: number): number {
  let at = start + 1;
  while (at < source.length) {
    if (source[at] === '"') return at + 1;
    at += source[at] === "\\" ? 2 : 1;
  }
  return at;
}
// The owning API already validated this JSON. Index ranges without parsing a recursive value or converting numbers.
function valueEnd(source: string, start: number): number {
  if (source[start] === '"') return stringEnd(source, start);
  let at = start;
  if (source[at] === "[" || source[at] === "{") {
    let depth = 1;
    at++;
    while (at < source.length && depth > 0) {
      const char = source[at];
      if (char === '"') {
        at = stringEnd(source, at);
        continue;
      }
      if (char === "[" || char === "{") depth++;
      else if (char === "]" || char === "}") depth--;
      at++;
    }
  } else {
    while (at < source.length && !",]} \t\r\n".includes(source[at])) at++;
  }
  return at;
}
function spaceEnd(source: string, start: number): number {
  while (start < source.length && " \t\r\n".includes(source[start])) start++;
  return start;
}
// No child nodes or key strings are allocated by the count-only visitor. Returning false stops at a page boundary.
function scanChildren(
  value: DataNode,
  visit: (
    start: number,
    end: number,
    keyStart: number,
    keyEnd: number,
    position: number,
  ) => boolean | void,
) {
  const { source, end, kind } = value;
  let at = spaceEnd(source, value.start + 1),
    position = 0;
  while (at < end - 1) {
    let keyStart = at,
      keyEnd = at;
    if (kind === "object") {
      keyEnd = stringEnd(source, at);
      at = spaceEnd(source, spaceEnd(source, keyEnd) + 1);
    }
    const start = at;
    at = valueEnd(source, at);
    if (visit(start, at, keyStart, keyEnd, position++) === false) return;
    at = spaceEnd(source, at);
    if (source[at] !== ",") return;
    at = spaceEnd(source, at + 1);
  }
}
export function dataCount(value: DataNode): number {
  let count = 0;
  if (value.kind === "array" || value.kind === "object")
    scanChildren(value, () => {
      count++;
    });
  return count;
}
export function dataChildren(value: DataNode, limit = 50) {
  const children: { label: string; index: boolean; value: DataNode }[] = [];
  if (limit <= 0 || (value.kind !== "array" && value.kind !== "object"))
    return children;
  scanChildren(value, (start, end, keyStart, keyEnd, position) => {
    const index = value.kind === "array";
    children.push({
      label: index ? String(position) : value.source.slice(keyStart, keyEnd),
      index,
      value: node(value.source, start, end),
    });
    return children.length < limit;
  });
  return children;
}
export function dataText(value: DataNode): string {
  return value.source.slice(value.start, value.end);
}
// Keep strings human-readable as before, decoding only individual quoted tokens. Number tokens stay verbatim.
// This is presentation only: actions, size and transport always use the untouched canonical source.
export function displayJSONStrings(source: string): string {
  let display = "",
    at = 0;
  while (at < source.length) {
    const start = source.indexOf('"', at);
    if (start < 0) return display + source.slice(at);
    const end = stringEnd(source, start);
    display +=
      source.slice(at, start) +
      JSON.stringify(JSON.parse(source.slice(start, end)));
    at = end;
  }
  return display;
}

// Data is canonical encoded text, not a native JS value. Copy, Download, byte thresholds and forwarding use it unchanged.
export function postData(json: JSONData) {
  const value = node(json, 0, json.length);
  const collection = value.kind === "array" || value.kind === "object";
  const summary = collection
    ? `${dataCount(value)} ${value.kind === "array" ? "items" : "keys"}`
    : value.kind;
  const bytes = new TextEncoder().encode(json).byteLength;
  return { json, bytes, inline: bytes <= 300, summary, collection, value };
}
