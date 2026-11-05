import type { JSONValue } from "./contract.gen.ts";

// JSONValue is validated by the post API; rendering never interprets post text as data.
export function postData(value: JSONValue) {
 const json = JSON.stringify(value);
 const bytes = new TextEncoder().encode(json).byteLength;
 const collection = value !== null && typeof value === "object";
 const keys = collection ? Object.keys(value) : [];
 const summary = Array.isArray(value) ? `${value.length} items` : collection ? `${keys.length} keys` : value === null ? "null" : typeof value;
 return { json, bytes, inline: bytes <= 300, summary, collection };
}
