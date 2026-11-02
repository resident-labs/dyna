import { Marked, type Token, type TokenizerAndRendererExtension, type Tokens } from "marked";
import { mentionName, mentionStart } from "./mentions.ts";

// A candidate @name passes through parsing and sanitizing as text between two Unicode noncharacters. Parse removes those
// from the source, so no author can write one. Parsing knows no room and its result is cached by text, so showMentions
// resolves candidates as each post shows. Marked's state keeps links and raw HTML out, and it never tokenizes code inline.
const mention: TokenizerAndRendererExtension = {
  name: "mention",
  level: "inline",
  start: mentionStart,
  tokenizer(src, tokens) {
    const { inLink, inRawBlock } = this.lexer.state;
    // A web address after the @ stays the link it always was.
    if (src[0] !== "@" || inLink || inRawBlock || /^@(?:https?:\/\/|ftp:\/\/|www\.)/i.test(src)) return undefined;
    const name = mentionName(tokens.at(-1)?.raw.at(-1), src.slice(1));
    return name ? { type: "mention", raw: `@${name}`, name } : undefined;
  },
  renderer: ({ name }) => `﷐${name}﷑`,
};

// Post images wait for the files view, so each one becomes a link to its web address, labeled with its alt text.
// An image without a web address keeps its label as plain text. An image inside a link, at any depth, becomes its
// label, so the link does not nest another link.
const markdown = new Marked({
  extensions: [mention],
  walkTokens(token) {
    if (token.type === "link" && token.tokens) token.tokens = labelImages(token.tokens);
  },
  renderer: {
    image({ href, text }: Tokens.Image): string {
      const label = escapeHtml(text || "Image");
      if (!/^https?:\/\//i.test(href)) return label;
      return `<a href="${escapeHtml(href)}">${label}</a>`;
    },
  },
});

// Replaces each image in a token list, and in the lists nested under it, with its label as text.
function labelImages(tokens: Token[]): Token[] {
  return tokens.map((child): Token => {
    if (child.type === "image") return { type: "text", raw: child.raw, text: child.text || "Image" };
    if ("tokens" in child && child.tokens) child.tokens = labelImages(child.tokens);
    return child;
  });
}

// Escapes HTML but keeps entities the Markdown source already wrote, such as &amp;, so each one shows once.
function escapeHtml(text: string): string {
  return text
    .replace(/&(?!(#\d{1,7}|#[Xx][a-fA-F0-9]{1,6}|\w+);)/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// Parses post Markdown into HTML. The shared Markdown component sanitizes the result before it reaches the page.
export function parse(text: string): string {
  return markdown.parse(text.replace(/[﷐﷑]/g, ""), { async: false, gfm: true, breaks: true });
}

// Each text renders once: a row that scrolls back in, or gets a new row object when a page loads, reuses its HTML. Parsing
// and sanitizing cost about a millisecond a post, which was most of a scroll frame. The feed keeps at most 1,000 events,
// so a full cache starts over. A value is the HTML split around its candidates, with the names at the odd places.
export function renderOnce(render: (text: string) => string): (text: string) => string[] {
  const rendered = new Map<string, string[]>();
  return text => {
    let parts = rendered.get(text);
    if (parts === undefined) {
      if (rendered.size >= 2000) rendered.clear();
      parts = render(text).split(/﷐(.*?)﷑/);
      rendered.set(text, parts);
    }
    return parts;
  };
}

// Joins a rendered text for one room: the name of a session links to it, and any other name stays plain text. This runs
// after sanitizing, which removes data-mention, so only this code writes it. A name holds only a mention's characters.
export function showMentions(parts: string[], room: string, sessions: ReadonlyMap<string, string>): string {
  return parts.map((part, place) => {
    if (place % 2 === 0) return part;
    const id = sessions.get(part);
    return id ? `<a href="/r/${encodeURIComponent(room)}/s/${encodeURIComponent(id)}" data-mention="${part}">@${part}</a>` : `@${part}`;
  }).join("");
}
