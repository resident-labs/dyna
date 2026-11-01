// Puts a Copy code row above each code block. The row takes its own space, so the button never covers code. Run it
// after sanitizing: the row markup is ours, and markdown never nests a pre, so each block ends at the first closing pre tag.
export function withCopyButtons(html: string): string {
  return html.replace(/<pre\b[\s\S]*?<\/pre>/g, (block) =>
    `<div class="code-block"><div class="code-head"><button class="code-copy" type="button">Copy code</button></div>${block}</div>`);
}
