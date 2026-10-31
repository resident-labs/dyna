// The sessions an owner's draft mentions, by v1's composer rule (internal/roomsvc/compose.go). Leading
// @names leave the text, as the feed shows them on the name line. A later @name is a mention when it
// starts the text or follows a space or "(", and stays in the text. A mention may end in punctuation
// ("@lead," is lead). Names inside `code`, after a backslash or a letter (an email), or of no open
// session stay plain text. Each name counts once.
export function parseMentions(draft: string, names: ReadonlySet<string>): { text: string; mentions: string[] } {
  const mentions: string[] = [];
  const add = (token: string) => {
    const name = token.replace(/(?<=.)[.,;:!?]+$/, "");
    if (!names.has(name)) return false;
    if (!mentions.includes(name)) mentions.push(name);
    return true;
  };
  let text = draft.trim();
  for (let lead = /^@(\S+)\s*/.exec(text); lead && add(lead[1]); lead = /^@(\S+)\s*/.exec(text)) text = text.slice(lead[0].length);
  let code = 0;
  for (let i = 0; i < text.length; i++) {
    if (text[i] === "`") {
      // A code span closes only with the backtick run that opened it.
      let end = i;
      while (text[end] === "`") end++;
      code = code === 0 ? end - i : code === end - i ? 0 : code;
      i = end - 1;
    } else if (!code && text[i] === "@" && (i === 0 || " \t\n(".includes(text[i - 1]))) {
      const token = /^[A-Za-z0-9._:-]+/.exec(text.slice(i + 1));
      if (token) add(token[0]);
    }
  }
  // A draft of mentions only keeps its words, so the post is never empty.
  return { text: text || draft.trim(), mentions };
}
