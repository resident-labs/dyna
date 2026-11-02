export type Json = string | number | boolean | null | Json[] | { [key: string]: Json };

// An object or a list, from text that is one whole. The engine masks a secret's value in place, which leaves "token": [masked]
// with no quotes, so that form reads too. Anything else is text, a bare number included.
export function parsedJson(text: string): Json | undefined {
  const trimmed = text.trim();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return undefined;
  for (const attempt of [trimmed, trimmed.replace(/:\s*\[masked\]/g, ': "[masked]"')]) {
    try {
      const value = JSON.parse(attempt) as Json;
      if (typeof value === "object" && value !== null) return value;
    } catch { /* Most text is not JSON. */ }
  }
  return undefined;
}
