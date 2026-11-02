// The sessions an owner's draft mentions, by v1's composer rule (internal/roomsvc/compose.go). The text
// is sent as typed; recipients are the mentions field. An @name is a mention when it starts the text or
// follows a space or "(". A mention may end in punctuation ("@lead," is lead). Names inside `code`,
// after a letter (an email), or of no open session stay plain text. Outside code, "\@" sends a plain "@".
// Each name counts once. The engine resolves a bare name in the post's room. It reads @parent, @children
// and @siblings as groups and @root as the room's root, so a session with a group name, or a session
// named root that is not the room's root, gets no mention.
const groups = new Set(["parent", "children", "siblings"]);
export const mentionable = ({ name, parent }: { name: string; parent?: string }) => !groups.has(name) && !(name === "root" && parent);
const opensName = (before: string | undefined) => !before || " \t\n(".includes(before);
// The name an @ opens, given the character before it and the text after it. Empty when it opens none.
export function mentionName(before: string | undefined, after: string): string {
  return opensName(before) ? (/^[A-Za-z0-9._:-]+/.exec(after)?.[0] ?? "").replace(/(?<=.)[.,;:!?]+$/, "") : "";
}
// Where the first @ that can open a name stands in the text, or -1.
export function mentionStart(text: string): number {
  for (let at = text.indexOf("@"); at >= 0; at = text.indexOf("@", at + 1)) if (opensName(text[at - 1])) return at;
  return -1;
}
// The session each @name shown in a post opens, as id by name. Names outlive closed sessions, so a closed session still
// opens, unless an open session has its name now.
export function mentionTargets(names: Iterable<[string, string]>, sessions: ReadonlyMap<string, { name: string; parent?: string }>): Map<string, string> {
  const targets = new Map<string, string>();
  for (const [id, name] of names) if (!sessions.has(id) && mentionable({ name })) targets.set(name, id);
  for (const [id, session] of sessions) if (mentionable(session)) targets.set(session.name, id);
  return targets;
}
export function parseMentions(draft: string, sessions: Iterable<{ name: string; parent?: string }>): { text: string; mentions: string[] } {
  const names = new Set([...sessions].filter(mentionable).map(({ name }) => name));
  const mentions: string[] = [];
  const text = draft.trim();
  let out = "";
  let code = 0;
  for (let i = 0; i < text.length; i++) {
    if (text[i] === "`") {
      // A code span closes only with the backtick run that opened it.
      let end = i;
      while (text[end] === "`") end++;
      code = code === 0 ? end - i : code === end - i ? 0 : code;
      out += text.slice(i, end);
      i = end - 1;
      continue;
    }
    if (!code && text[i] === "\\" && text[i + 1] === "@") continue;
    if (!code && text[i] === "@") {
      const name = mentionName(text[i - 1], text.slice(i + 1));
      if (names.has(name) && !mentions.includes(name)) mentions.push(name);
    }
    out += text[i];
  }
  return { text: out, mentions };
}

// The @word the caret ends, by the same rule: it starts the text or follows a space, a line break or "(".
// The composer offers the sessions whose names start with its query.
export function mentionToken(text: string, caret: number): { start: number; query: string } | undefined {
  const match = /(?:^|[\s(])@([A-Za-z0-9._:-]*)$/.exec(text.slice(0, caret));
  return match ? { start: caret - match[1].length - 1, query: match[1] } : undefined;
}
