import type { LanguageFn } from "highlight.js";

const languages: Record<string, () => Promise<{ default: LanguageFn }>> = {
  go: () => import("highlight.js/lib/languages/go"),
  javascript: () => import("highlight.js/lib/languages/javascript"),
  typescript: () => import("highlight.js/lib/languages/typescript"),
  json: () => import("highlight.js/lib/languages/json"),
  bash: () => import("highlight.js/lib/languages/bash"),
  python: () => import("highlight.js/lib/languages/python"),
  yaml: () => import("highlight.js/lib/languages/yaml"),
  diff: () => import("highlight.js/lib/languages/diff"),
  css: () => import("highlight.js/lib/languages/css"),
  xml: () => import("highlight.js/lib/languages/xml"),
  sql: () => import("highlight.js/lib/languages/sql"),
  rust: () => import("highlight.js/lib/languages/rust"),
  markdown: () => import("highlight.js/lib/languages/markdown"),
};
const aliases: Record<string, string> = { js: "javascript", ts: "typescript", sh: "bash", shell: "bash", py: "python", yml: "yaml", html: "xml", rs: "rust", md: "markdown" };
const escape = (text: string) => text.replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c] ?? c);

// Returns escaped HTML even when the language is unknown or the source is too large.
export async function highlight(source: string, language: string): Promise<string> {
  const name = language.toLowerCase();
  const key = Object.hasOwn(aliases, name) ? aliases[name] : name;
  if (!Object.hasOwn(languages, key) || source.length > 300_000) return escape(source);
  const [core, grammar] = await Promise.all([import("highlight.js/lib/core"), languages[key]()]);
  if (!core.default.getLanguage(key)) core.default.registerLanguage(key, grammar.default);
  return core.default.highlight(source, { language: key, ignoreIllegals: true }).value;
}
