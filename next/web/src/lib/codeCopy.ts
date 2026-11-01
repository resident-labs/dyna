// Add our own copy control after sanitizing. Markdown never nests a pre, so each block ends at the first closing pre tag.
export function withCopyButtons(html: string, copyPath: string): string {
  return html.replace(/<pre\b[\s\S]*?<\/pre>/g, (block) =>
    `<div class="code-block"><div class="code-head"><button class="code-copy" type="button" aria-label="Copy code" title="Copy code"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${copyPath}"></path></svg></button></div>${block}</div>`);
}
