import { Marked, type Tokens } from "marked";

// Post images wait for the files view, so each one becomes a link to its web address, labeled with its alt text.
// An image without a web address keeps its label as plain text, so no link points anywhere unsafe.
const markdown = new Marked({
  renderer: {
    image({ href, text }: Tokens.Image): string {
      const label = escapeHtml(text || "Image");
      if (!/^https?:\/\//i.test(href)) return label;
      return `<a href="${escapeHtml(href)}">${label}</a>`;
    },
  },
});

function escapeHtml(text: string): string {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// Parses post Markdown into HTML. The shared Markdown component sanitizes the result before it reaches the page.
export function parse(text: string): string {
  return markdown.parse(text, { async: false, gfm: true, breaks: true });
}
