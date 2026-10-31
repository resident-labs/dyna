export const ruleLimit = 2000; // engine/rules.go refuses a longer rule.

// Words whose period ends no sentence.
const noStop = /(?:^|[\s(])(?:r\d+|e\.g|i\.e|etc|vs|cf)$/i;

// A rule's first sentence reads in the text color and the rest muted. Only a period, a space and a capital letter end
// the sentence; a rule of one sentence has no rest.
export function splitRule(text: string): { lead: string; rest: string } {
  for (const match of text.matchAll(/\. (?=[A-Z])/g)) {
    if (!noStop.test(text.slice(0, match.index))) return { lead: text.slice(0, match.index + 1), rest: text.slice(match.index + 1) };
  }
  return { lead: text, rest: "" };
}

export type RulePart = { kind: "text" | "code"; text: string } | { kind: "link"; text: string; number: number };

// Cuts a rule at its code spans and at its references to active rules. A number inside code, a path or quotes, or one
// that no active rule has, stays plain text.
export function ruleParts(text: string, active: { has(number: number): boolean }): RulePart[] {
  const parts: RulePart[] = [];
  const plain = (value: string) => { if (value) parts.push({ kind: "text", text: value }); };
  text.split(/(`[^`\n]+`)/).forEach((piece, i) => {
    if (i % 2) { parts.push({ kind: "code", text: piece.slice(1, -1) }); return; }
    let at = 0;
    for (const match of piece.matchAll(/(?<![\w/.'"-])r(\d+)(?![\w/'"-]|\.\w)/g)) {
      const number = Number(match[1]);
      if (!active.has(number)) continue;
      plain(piece.slice(at, match.index));
      parts.push({ kind: "link", text: match[0], number });
      at = match.index + match[0].length;
    }
    plain(piece.slice(at));
  });
  return parts;
}

// Cuts text at each match of a search, whatever its case.
export function marks(text: string, query: string): { text: string; hit: boolean }[] {
  const cuts: { text: string; hit: boolean }[] = [];
  const lower = text.toLowerCase(), wanted = query.toLowerCase();
  let at = 0;
  for (let hit = wanted ? lower.indexOf(wanted) : -1; hit >= 0; hit = lower.indexOf(wanted, at)) {
    if (hit > at) cuts.push({ text: text.slice(at, hit), hit: false });
    cuts.push({ text: text.slice(hit, hit + wanted.length), hit: true });
    at = hit + wanted.length;
  }
  if (at < text.length) cuts.push({ text: text.slice(at), hit: false });
  return cuts;
}

// Rules ride in every prompt. A token is about 4 characters, so the figure is rounded and shown as about.
export function ruleTokens(characters: number): string {
  const tokens = characters / 4, step = tokens < 1000 ? 10 : 100;
  return (Math.round(tokens / step) * step).toLocaleString("en-US");
}

// A rule's age as a short citation: "5m", "3h", "8d".
export function ruleAge(at: string, now: number): string {
  const minutes = Math.max(0, Math.floor((now - Date.parse(at)) / 60_000));
  if (minutes < 1) return "now";
  if (minutes < 60) return `${minutes}m`;
  return minutes < 1440 ? `${Math.floor(minutes / 60)}h` : `${Math.floor(minutes / 1440)}d`;
}
