import type { JSONValue } from "./contract.gen.ts";

// JSONValue is validated by the post API; rendering never interprets post text as data.
export function postData(value: JSONValue) {
 // Match the post boundary's compact JSON encoding, including HTML-sensitive characters and script separators.
 const json = JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, char => `\\u${char.charCodeAt(0).toString(16).padStart(4, "0")}`);
 const bytes = new TextEncoder().encode(json).byteLength;
 const array = Array.isArray(value);
 const collection = value !== null && typeof value === "object";
 const keys = collection && !array ? Object.keys(value) : [];
 const summary = array ? `${value.length} items` : collection ? `${keys.length} keys` : value === null ? "null" : typeof value;
 return { json, bytes, inline: bytes <= 300, summary, collection };
}
