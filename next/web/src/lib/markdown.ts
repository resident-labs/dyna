import { marked } from "marked";

// Parses post Markdown into HTML. The shared Markdown component sanitizes the result before it reaches the page.
export function parse(text: string): string {
  return marked.parse(text, { async: false, gfm: true, breaks: true });
}
