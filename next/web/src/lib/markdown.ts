import { Marked, type Token, type Tokens } from "marked";

// Post images wait for the files view, so each one becomes a link to its web address, labeled with its alt text.
// An image without a web address keeps its label as plain text. An image inside a link is only its label, so the
// link does not nest another link.
const markdown = new Marked({
  walkTokens(token) {
    if (token.type !== "link") return;
    token.tokens = token.tokens.map((child): Token => child.type === "image" ? { type: "text", raw: child.raw, text: child.text || "Image" } : child);
  },
  renderer: {
    image({ href, text }: Tokens.Image): string {
      const label = escapeHtml(text || "Image");
      if (!/^https?:\/\//i.test(href)) return label;
      return `<a href="${escapeHtml(href)}">${label}</a>`;
    },
  },
});

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
  return markdown.parse(text, { async: false, gfm: true, breaks: true });
}
