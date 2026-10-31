// The sessions an owner's draft mentions, by v1's composer rule (internal/roomsvc/compose.go). The text
// is sent as typed; recipients are the mentions field. An @name is a mention when it starts the text or
// follows a space or "(". A mention may end in punctuation ("@lead," is lead). Names inside `code`,
// after a letter (an email), or of no open session stay plain text. Outside code, "\@" sends a plain "@".
// Each name counts once. The engine resolves a bare name in the post's room. It reads @parent, @children
// and @siblings as groups, so a session with one of those names gets no mention.
const groups = new Set(["parent", "children", "siblings"]);
export function parseMentions(draft: string, names: ReadonlySet<string>): { text: string; mentions: string[] } {
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
    if (!code && text[i] === "@" && (i === 0 || " \t\n(".includes(text[i - 1]))) {
      const name = (/^[A-Za-z0-9._:-]+/.exec(text.slice(i + 1))?.[0] ?? "").replace(/(?<=.)[.,;:!?]+$/, "");
      if (names.has(name) && !groups.has(name) && !mentions.includes(name)) mentions.push(name);
    }
    out += text[i];
  }
  return { text: out, mentions };
}
