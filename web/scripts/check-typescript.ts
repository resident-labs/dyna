// Policy check for web TypeScript: rejects type escapes that hide real type errors,
// and rejects JavaScript files under the checked source directories.
// Run with Node's built-in type stripping, so this file uses erasable syntax only.
//   node scripts/check-typescript.ts [path...]
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import type { Dirent } from "node:fs";
import { extname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { parse } from "svelte/compiler";
import ts from "typescript";

export type Rule = "no-explicit-any" | "no-double-cast" | "no-ts-suppression" | "no-non-null-assertion" | "no-javascript-file";

export interface Violation {
  file: string;
  line: number;
  column: number;
  rule: Rule;
  message: string;
}

const DEFAULT_DIRS = ["src", "scripts", "test", "tests"];
const CONFIG_FILE = /\.config\.[mc]?ts$/;
// Svelte template node types; any other node type is a JavaScript or TypeScript (ESTree) node.
const SVELTE_NODE_TYPE = /^(Svelte|Title)|(Block|Tag|Directive|Element)$|^(Fragment|Text|Comment|Attribute|SpreadAttribute|Component|Script|StyleSheet|Root)$/;
const SKIPPED_DIRS = new Set(["node_modules", "dist", ".git"]);
const TS_EXTENSIONS = new Set([".ts", ".mts", ".cts", ".tsx"]);
const JAVASCRIPT_EXTENSIONS = new Set([".js", ".mjs", ".cjs"]);
const SCRIPT_TAG = /<script\b(?<attributes>(?:"[^"]*"|'[^']*'|[^>"'])*)>/gi;
const LANG_TS = /\blang\s*=\s*["']?ts["']?(?=[\s"'>]|$)/i;
const SUPPRESSION = /^[\s/*]*@ts-(ignore|expect-error|nocheck)\b/m;

function isJSDocKind(kind: ts.SyntaxKind): boolean {
  return kind >= ts.SyntaxKind.FirstJSDocNode && kind <= ts.SyntaxKind.LastJSDocNode;
}

// Skips parentheses so `(x as unknown) as T` counts as a double cast.
function unwrapParens(node: ts.Expression): ts.Expression {
  return ts.isParenthesizedExpression(node) ? unwrapParens(node.expression) : node;
}

function isDoubleCastThroughUnknown(node: ts.Node): boolean {
  if (!ts.isAsExpression(node) && !ts.isTypeAssertionExpression(node)) return false;
  const inner = unwrapParens(node.expression);
  return (ts.isAsExpression(inner) || ts.isTypeAssertionExpression(inner)) && inner.type.kind === ts.SyntaxKind.UnknownKeyword;
}

/** Checks TypeScript source text. `fileName` picks the parser mode (.tsx) and labels violations. */
export function checkSource(text: string, fileName: string): Violation[] {
  const scriptKind = extname(fileName) === ".tsx" ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sourceFile = ts.createSourceFile(fileName, text, ts.ScriptTarget.Latest, true, scriptKind);
  const violations: Violation[] = [];
  const seenComments = new Set<number>();

  const report = (pos: number, rule: Rule, message: string): void => {
    const { line, character } = sourceFile.getLineAndCharacterOfPosition(pos);
    violations.push({ file: fileName, line: line + 1, column: character + 1, rule, message });
  };

  const scanComments = (pos: number): void => {
    for (const range of [...(ts.getLeadingCommentRanges(text, pos) ?? []), ...(ts.getTrailingCommentRanges(text, pos) ?? [])]) {
      if (seenComments.has(range.pos)) continue;
      seenComments.add(range.pos);
      const match = SUPPRESSION.exec(text.slice(range.pos, range.end));
      if (match) report(range.pos, "no-ts-suppression", `@ts-${match[1] ?? "ignore"} comment hides a type error`);
    }
  };

  const visit = (node: ts.Node): void => {
    if (isJSDocKind(node.kind)) return;
    scanComments(node.pos);
    if (node.kind === ts.SyntaxKind.AnyKeyword) {
      report(node.getStart(sourceFile), "no-explicit-any", "explicit any; use unknown or a real type");
    } else if (ts.isNonNullExpression(node)) {
      report(node.end - 1, "no-non-null-assertion", "non-null assertion (!); narrow the value instead");
    } else if ((ts.isVariableDeclaration(node) || ts.isPropertyDeclaration(node)) && node.exclamationToken) {
      report(node.exclamationToken.getStart(sourceFile), "no-non-null-assertion", "definite assignment assertion (!); initialize the value instead");
    } else if (isDoubleCastThroughUnknown(node)) {
      report(node.getStart(sourceFile), "no-double-cast", "double cast through unknown; validate or narrow instead");
    }
    for (const child of node.getChildren(sourceFile)) visit(child);
  };

  visit(sourceFile);
  return violations.sort((a, b) => a.line - b.line || a.column - b.column);
}

// Blanks every character except newlines, so later text keeps its original line and column.
function blank(text: string): string {
  return text.replace(/[^\n]/g, " ");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// Finds the top-level TypeScript nodes of a Svelte template: expressions, patterns and declarations.
function collectTemplateSlices(node: unknown, found: { start: number; end: number; type: string }[]): void {
  if (Array.isArray(node)) {
    node.forEach((item) => collectTemplateSlices(item, found));
    return;
  }
  if (!isRecord(node)) return;
  const { type, start, end } = node;
  if (typeof type === "string" && typeof start === "number" && typeof end === "number" && !SVELTE_NODE_TYPE.test(type)) {
    found.push({ start, end, type });
    return;
  }
  Object.values(node).forEach((child) => collectTemplateSlices(child, found));
}

/**
 * Checks a Svelte file that has a `<script lang="ts">` block: the script bodies and every
 * template expression. Plain JavaScript components are skipped. Positions refer to the whole file.
 */
export function checkSvelte(text: string, fileName: string): Violation[] {
  const violations: Violation[] = [];
  const inFile = (v: Violation): Violation => ({ ...v, file: fileName });
  if (![...text.matchAll(SCRIPT_TAG)].some((tag) => LANG_TS.test(tag.groups?.attributes ?? ""))) return violations;
  const root = parse(text, { modern: true });
  let hasTypeScript = false;
  for (const script of [root.instance, root.module]) {
    if (!script) continue;
    const language = script.attributes.find((attribute) => attribute.name === "lang");
    if (!language || !Array.isArray(language.value) || !language.value.some((part) => part.type === "Text" && part.data === "ts")) continue;
    hasTypeScript = true;
    const content = script.content;
    if (!("start" in content) || typeof content.start !== "number" || !("end" in content) || typeof content.end !== "number") {
      throw new Error(`Missing script positions in ${fileName}`);
    }
    const { start, end } = content;
    violations.push(...checkSource(blank(text.slice(0, start)) + text.slice(start, end), `${fileName}.ts`).map(inFile));
    const generics = script.attributes.find((attribute) => attribute.name === "generics");
    if (generics && Array.isArray(generics.value)) {
      for (const part of generics.value) {
        if (part.type !== "Text") continue;
        // Parse generic parameters in a declaration, preserving their source positions.
        const prefix = "type T<";
        const declaration = blank(text.slice(0, part.start - prefix.length)) + prefix + part.data + "> = unknown";
        violations.push(...checkSource(declaration, `${fileName}.ts`).map(inFile));
      }
    }
  }
  if (!hasTypeScript) return violations;

  const slices: { start: number; end: number; type: string }[] = [];
  collectTemplateSlices(root.fragment, slices);
  for (const { start, end, type } of slices) {
    // Wrap expressions in parentheses, placed over the character before the slice so positions stay put.
    const [open, close] = type.endsWith("Declaration") ? ["", ""] : ["(", ")"];
    const padded = blank(text.slice(0, start - open.length)) + open + text.slice(start, end) + close;
    violations.push(...checkSource(padded, `${fileName}.ts`).map(inFile));
  }
  return violations.sort((a, b) => a.line - b.line || a.column - b.column);
}

/** Checks one file from disk. Files of other types return no violations. */
export function checkFile(path: string): Violation[] {
  const extension = extname(path);
  if (extension === ".svelte") return checkSvelte(readFileSync(path, "utf8"), path);
  if (TS_EXTENSIONS.has(extension)) return checkSource(readFileSync(path, "utf8"), path);
  return [];
}

/** Expands files and directories into a sorted list of checkable files. */
export function collectFiles(paths: string[]): string[] {
  const found: string[] = [];
  const walk = (path: string): void => {
    if (statSync(path).isDirectory()) {
      for (const entry of readdirSync(path).sort()) {
        if (!SKIPPED_DIRS.has(entry)) walk(join(path, entry));
      }
    } else if (extname(path) === ".svelte" || TS_EXTENSIONS.has(extname(path))) {
      found.push(path);
    }
  };
  paths.forEach(walk);
  return found;
}

/** Finds JavaScript files under `paths`, skipping the same directories as `collectFiles`. */
function findJavaScriptFiles(paths: string[]): string[] {
  const found: string[] = [];
  const walk = (path: string): void => {
    if (!existsSync(path)) return;
    if (statSync(path).isDirectory()) {
      for (const entry of readdirSync(path).sort()) {
        if (!SKIPPED_DIRS.has(entry)) walk(join(path, entry));
      }
    } else if (JAVASCRIPT_EXTENSIONS.has(extname(path))) {
      found.push(path);
    }
  };
  paths.forEach(walk);
  return found;
}

/** Rejects every JavaScript file under `paths`: those files must be TypeScript. */
export function checkJavaScriptOnly(paths: string[]): Violation[] {
  return findJavaScriptFiles(paths).map((file) => ({
    file,
    line: 1,
    column: 1,
    rule: "no-javascript-file",
    message: "JavaScript file; convert it to TypeScript",
  }));
}

export function formatViolation(v: Violation): string {
  return `${v.file}:${v.line}:${v.column} ${v.rule}: ${v.message}`;
}

/** Default targets under `dir`: the source directories that exist, plus root `*.config.ts` files. */
export function defaultRoots(dir: string): string[] {
  const isTarget = (entry: Dirent): boolean =>
    entry.isDirectory() ? DEFAULT_DIRS.includes(entry.name) : CONFIG_FILE.test(entry.name);
  return readdirSync(dir, { withFileTypes: true })
    .filter(isTarget)
    .map((entry) => join(dir, entry.name))
    .sort();
}

/** CLI entry. Returns the process exit code: 0 clean, 1 violations. */
export function main(args: string[]): number {
  const roots = args.length > 0 ? args : defaultRoots(".");
  const violations = [...checkJavaScriptOnly(roots), ...collectFiles(roots).flatMap(checkFile)];
  for (const violation of violations) console.error(formatViolation(violation));
  if (violations.length > 0) {
    console.error(`${violations.length} TypeScript policy violation(s)`);
    return 1;
  }
  return 0;
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
