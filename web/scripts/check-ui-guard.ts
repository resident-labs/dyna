// Guard for new web code: rejects lines a change ADDS that skip the shared
// design parts. Old code is not swept; only the diff from the base counts.
//   1. A literal font size (font-size: 13px, or px in a font shorthand) in a
//      .svelte or .css file. Use a --t-* token from lib/ui/tokens.css.
//   2. A private card style outside web/src/lib/ui/: a rule whose selector
//      names .card, .panel, .group or .tile and that sets a background, a
//      border or an inset box-shadow. Use Card, Section or StatTile.
// A justified case opts out with a comment on the same line:
//   /* ui-guard: allow <reason> */   or, in markup, <!-- ui-guard: allow <reason> -->
// The base is --base <rev>, else DYNA_CI_BASE (local CI), else the first parent
// of a merge commit (a GitHub pull request run), else the merge base with
// origin/main. Run with Node's type stripping:
//   node scripts/check-ui-guard.ts [--base <rev>]
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

export type GuardRule = "literal-font-size" | "private-card-style";

export interface GuardViolation {
  file: string;
  line: number;
  rule: GuardRule;
  message: string;
}

// Added line numbers per file, from a unified diff with paths relative to the repository root.
export function addedLines(diff: string): Map<string, Set<number>> {
  const added = new Map<string, Set<number>>();
  let lines: Set<number> | null = null;
  let next = 0;
  for (const text of diff.split("\n")) {
    if (text.startsWith("+++ ")) {
      const path = text.slice(4);
      lines = path === "/dev/null" ? null : new Set<number>();
      if (lines) added.set(path.replace(/^b\//, ""), lines);
      continue;
    }
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(text);
    if (hunk) {
      next = Number(hunk[1]);
      continue;
    }
    if (!lines || text.startsWith("--- ")) continue;
    if (text.startsWith("+")) lines.add(next++);
    else if (text.startsWith(" ")) next++;
  }
  return added;
}

const OPT_OUT = /ui-guard:\s*allow\s+\S/;
const FONT_SIZE = /\bfont-size\s*:\s*(\d*\.?\d+)px\b/;
const FONT_SHORTHAND = /\bfont\s*:[^;"]*?(?<![\w.-])(\d*\.?\d+)px\b/;
const CARD_SELECTOR = /\.(card|panel|group|tile)(?![\w-])/;
const SURFACE = /^(background(-color)?|border(-(top|right|bottom|left|block|inline)(-(start|end))?)?(-(width|style|color))?)$/;
const EMPTY_VALUE = /^(none|0|transparent|inherit|unset|initial)$/;

// Type tokens by pixel size, read from tokens.css: --t-ui: 13px gives 13 → --t-ui.
export function typeTokens(css: string): Map<number, string> {
  const tokens = new Map<number, string>();
  for (const match of css.matchAll(/(--t-[\w-]+)\s*:\s*(\d*\.?\d+)px/g)) {
    const [, name, size] = match;
    if (name !== undefined && size !== undefined && !tokens.has(Number(size))) tokens.set(Number(size), name);
  }
  return tokens;
}

function nearestToken(px: number, tokens: Map<number, string>): string {
  let best = "";
  let gap = Infinity;
  for (const [size, name] of tokens) {
    if (Math.abs(size - px) < gap) [best, gap] = [`var(${name}) (${size}px)`, Math.abs(size - px)];
  }
  return best || "a --t-* token from lib/ui/tokens.css";
}

interface Declaration { property: string; value: string; line: number }
interface CssRule { selector: string; firstLine: number; lastLine: number; declarations: Declaration[] }

const blank = (text: string): string => text.replace(/[^\n]/g, " ");

// The CSS of a file: the whole of a .css file, the <style> bodies of a
// .svelte file. Other text is blanked, so line numbers stay put.
function cssOf(path: string, text: string): string {
  if (path.endsWith(".css")) return text;
  let out = "";
  let last = 0;
  for (const match of text.matchAll(/(<style\b[^>]*>)([\s\S]*?)<\/style>/g)) {
    const [, open = "", body = ""] = match;
    const bodyStart = match.index + open.length;
    out += blank(text.slice(last, bodyStart)) + body;
    last = bodyStart + body.length;
  }
  return out + blank(text.slice(last));
}

// A small block scanner: every "{" opens a block with its prelude; text
// between ";" in a rule block is one declaration. Comments are blanked first.
export function cssRules(css: string): CssRule[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, blank);
  const rules: CssRule[] = [];
  const stack: (CssRule | null)[] = [];
  let pos = 0;
  let line = 1;
  // Calls come in text order, so the line count only moves forward.
  const lineAt = (index: number): number => {
    for (; pos < index; pos++) if (text[pos] === "\n") line++;
    return line;
  };
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c !== "{" && c !== "}" && c !== ";") continue;
    const raw = text.slice(start, i);
    const body = raw.trim();
    const first = lineAt(start + raw.length - raw.trimStart().length);
    const last = lineAt(i);
    const top = stack.at(-1) ?? null;
    if (c === "{") {
      const rule = body.startsWith("@") ? null : { selector: body, firstLine: first, lastLine: last, declarations: [] };
      if (rule) rules.push(rule);
      stack.push(rule);
    } else if (body && top) {
      const colon = body.indexOf(":");
      if (colon > 0) top.declarations.push({ property: body.slice(0, colon).trim().toLowerCase(), value: body.slice(colon + 1).trim().toLowerCase(), line: first });
    }
    if (c === "}") stack.pop();
    start = i + 1;
  }
  return rules;
}

function surfaceDeclaration(d: Declaration): boolean {
  const value = d.value.replace(/\s*!important$/, "");
  if (d.property === "box-shadow") return /\binset\b/.test(value);
  return SURFACE.test(d.property) && !EMPTY_VALUE.test(value);
}

/** Checks one file's added lines. `path` is relative to the repository root. */
export function checkAdded(path: string, text: string, added: Set<number>, tokens: Map<number, string>): GuardViolation[] {
  if (!path.endsWith(".svelte") && !path.endsWith(".css")) return [];
  const source = text.split("\n");
  const allowed = (line: number): boolean => OPT_OUT.test(source[line - 1] ?? "");
  const violations: GuardViolation[] = [];
  const optOut = 'or add "/* ui-guard: allow <reason> */" on the line';
  for (const line of [...added].sort((a, b) => a - b)) {
    const content = source[line - 1] ?? "";
    const size = FONT_SIZE.exec(content) ?? FONT_SHORTHAND.exec(content);
    if (size?.[1] !== undefined && !allowed(line)) {
      violations.push({ file: path, line, rule: "literal-font-size", message: `literal font size ${size[1]}px; use ${nearestToken(Number(size[1]), tokens)} ${optOut}` });
    }
  }
  if (path.startsWith("web/src/lib/ui/")) return violations;
  for (const rule of cssRules(cssOf(path, text))) {
    const name = CARD_SELECTOR.exec(rule.selector)?.[0];
    if (!name) continue;
    const surfaces = rule.declarations.filter(surfaceDeclaration);
    if (!surfaces.length) continue;
    let selectorAdded = false;
    for (let l = rule.firstLine; l <= rule.lastLine; l++) if (added.has(l)) selectorAdded = true;
    const hits = selectorAdded ? [rule.firstLine] : surfaces.filter((d) => added.has(d.line)).map((d) => d.line);
    for (const line of hits) {
      if (allowed(line)) continue;
      violations.push({ file: path, line, rule: "private-card-style", message: `private card style "${name}" (${surfaces.map((d) => d.property).join(", ")}); use Card from lib/ui/Card.svelte (flush and divided for lists, Section for a titled group, StatTile in a .stat-grid for figures) ${optOut}` });
    }
  }
  return violations;
}

function git(root: string, args: string[]): string | null {
  try {
    return execFileSync("git", ["-C", root, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"], maxBuffer: 256 * 1024 * 1024 }).trim();
  } catch {
    return null;
  }
}

/** Picks the base commit; null when this checkout has none to compare with. */
export function findBase(root: string, args: string[], env: NodeJS.ProcessEnv): string | null {
  const flag = args.indexOf("--base");
  const asked = flag >= 0 ? args[flag + 1] : env.DYNA_CI_BASE;
  const verified = (rev: string | undefined): string | null => (rev ? git(root, ["rev-parse", "-q", "--verify", `${rev}^{commit}`]) : null);
  if (asked) return verified(asked);
  if (verified("HEAD^2")) return verified("HEAD^1");
  return git(root, ["merge-base", "HEAD", "origin/main"]);
}

/** CLI entry. Returns the process exit code: 0 clean or no base, 1 violations, 2 a bad base. */
export function main(args: string[], env: NodeJS.ProcessEnv = process.env): number {
  const root = git(".", ["rev-parse", "--show-toplevel"]);
  if (!root) {
    console.error("ui-guard: not a git checkout; skipped");
    return 0;
  }
  const base = findBase(root, args, env);
  if (!base) {
    if (args.includes("--base") || env.DYNA_CI_BASE) {
      console.error("ui-guard: the base commit is not in this checkout");
      return 2;
    }
    console.error("ui-guard: no base commit in this checkout (shallow clone?); skipped");
    return 0;
  }
  const diff = git(root, ["diff", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", "--find-renames", "-U0", base, "--", "web/*.svelte", "web/*.css"]) ?? "";
  const tokensFile = join(root, "web/src/lib/ui/tokens.css");
  const tokens = typeTokens(existsSync(tokensFile) ? readFileSync(tokensFile, "utf8") : "");
  const violations: GuardViolation[] = [];
  for (const [path, added] of addedLines(diff)) {
    if (added.size) violations.push(...checkAdded(path, readFileSync(join(root, path), "utf8"), added, tokens));
  }
  for (const v of violations) console.error(`${v.file}:${v.line} ${v.rule}: ${v.message}`);
  if (violations.length) {
    console.error(`${violations.length} UI guard violation(s) in lines added since ${base.slice(0, 12)}`);
    return 1;
  }
  return 0;
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
