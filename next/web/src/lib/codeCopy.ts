// Adds a Copy code button in front of each code block. Run it after sanitizing: the button markup is ours, and
// markdown never nests a pre, so each block ends at the first closing pre tag.
export function withCopyButtons(html: string): string {
  return html.replace(/<pre\b[\s\S]*?<\/pre>/g, (block) =>
    `<div class="code-block"><button class="code-copy" type="button">Copy code</button>${block}</div>`);
}
