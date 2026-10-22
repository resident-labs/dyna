import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { checkSource, checkSvelte, collectFiles, defaultRoots, main } from "./check-typescript.ts";

const rulesOf = (code: string): string[] => checkSource(code, "x.ts").map((v) => v.rule);

test("rejects explicit any in annotations, casts and generics", () => {
  assert.deepEqual(rulesOf("let a: any = 1;"), ["no-explicit-any"]);
  assert.deepEqual(rulesOf("const b = value as any;"), ["no-explicit-any"]);
  assert.deepEqual(rulesOf("const c: Array<any> = [];"), ["no-explicit-any"]);
  assert.deepEqual(rulesOf("const d = <any>value;"), ["no-explicit-any"]);
});

test("rejects double casts through unknown, including parenthesized", () => {
  assert.deepEqual(rulesOf("const a = x as unknown as Foo;"), ["no-double-cast"]);
  assert.deepEqual(rulesOf("const a = (x as unknown) as Foo;"), ["no-double-cast"]);
  assert.deepEqual(rulesOf("const a = x as unknown;"), []);
  for (const expression of ["<string><unknown>123", "(<unknown>123) as string", "<string>(123 as unknown)", "(123 as unknown) as string"]) {
    assert.deepEqual(rulesOf(`const a = ${expression};`), ["no-double-cast"]);
  }
});

test("rejects non-null assertions but not logical not or !==", () => {
  assert.deepEqual(rulesOf("const a = map.get(k)!;"), ["no-non-null-assertion"]);
  assert.deepEqual(rulesOf("const a = obj!.field;"), ["no-non-null-assertion"]);
  assert.deepEqual(rulesOf("const a = !flag && x !== y;"), []);
});

test("rejects definite assignment assertions on variables and class properties", () => {
  assert.deepEqual(rulesOf("let value!: string;"), ["no-non-null-assertion"]);
  assert.deepEqual(rulesOf("class X { value!: string; }"), ["no-non-null-assertion"]);
  assert.deepEqual(rulesOf("class X { value?: string; other: string = ''; }"), []);
});

test("rejects suppression comments, in every position", () => {
  assert.deepEqual(rulesOf("// @ts-ignore\nconst a = 1;"), ["no-ts-suppression"]);
  assert.deepEqual(rulesOf("const a = 1;\n// @ts-expect-error because\nfoo();"), ["no-ts-suppression"]);
  assert.deepEqual(rulesOf("/* @ts-ignore */ foo();"), ["no-ts-suppression"]);
  assert.deepEqual(rulesOf("function f() {\n  // @ts-ignore\n}"), ["no-ts-suppression"]);
  assert.deepEqual(rulesOf("foo();\n// @ts-nocheck"), ["no-ts-suppression"]);
  for (const directive of ["ignore", "expect-error"]) {
    assert.deepEqual(rulesOf(`const a = 1; // @ts-${directive}\nconst b: string = 123;`), ["no-ts-suppression"]);
  }
});

test("ignores strings, templates, regexes and ordinary comments", () => {
  const code = [
    'const a = "x as any, @ts-ignore, y!";',
    "const b = `// @ts-ignore ${1}`;",
    "const c = /as any/;",
    "// we never use any or as unknown as here",
    "/** the word any, and @ts-ignore mentioned mid-sentence */",
    "const d = 1;",
  ].join("\n");
  assert.deepEqual(rulesOf(code), []);
});

test("reports one-based line and column", () => {
  const violations = checkSource("const ok = 1;\n  let a: any;", "pos.ts");
  assert.deepEqual(violations.map((v) => [v.file, v.line, v.column]), [["pos.ts", 2, 10]]);
});

test("parses tsx files", () => {
  assert.deepEqual(checkSource("const a = <div>{x as any}</div>;", "x.tsx").map((v) => v.rule), ["no-explicit-any"]);
});

test("checks only lang=ts svelte scripts and keeps original positions", () => {
  const svelte = [
    "<script module>",
    "  let a = b!;",
    "</script>",
    "",
    '<script lang="ts" generics="T extends Array<string>">',
    "  let c: any;",
    "</script>",
    "<p>{x as any}</p>",
  ].join("\n");
  const violations = checkSvelte(svelte, "A.svelte");
  assert.deepEqual(
    violations.map((v) => [v.file, v.rule, v.line, v.column]),
    [
      ["A.svelte", "no-explicit-any", 6, 10],
      ["A.svelte", "no-explicit-any", 8, 10],
    ],
  );
});

test("checks template expressions of a lang=ts svelte file", () => {
  const svelte = [
    '<script lang="ts">',
    "  let items: string[] = [];",
    "</script>",
    "{#each items as item (item!)}",
    "  <button onclick={(e) => e.target!.focus()} title={item as unknown as number}>x</button>",
    "{/each}",
    "{#if items[0]!}",
    "  {@const n = (item as any).length}",
    "{/if}",
    "{#snippet row(cell: any)}{/snippet}",
  ].join("\n");
  assert.deepEqual(
    checkSvelte(svelte, "T.svelte").map((v) => [v.rule, v.line, v.column]),
    [
      ["no-non-null-assertion", 4, 27],
      ["no-non-null-assertion", 5, 35],
      ["no-double-cast", 5, 53],
      ["no-non-null-assertion", 7, 14],
      ["no-explicit-any", 8, 24],
      ["no-explicit-any", 10, 21],
    ],
  );
});

test("skips the template of a javascript svelte component", () => {
  assert.deepEqual(checkSvelte("<script>let a = 1;</script>\n{a!}", "J.svelte"), []);
});

test("defaultRoots finds source directories and root config files", () => {
  const dir = mkdtempSync(join(tmpdir(), "tscheck-"));
  try {
    mkdirSync(join(dir, "src"));
    mkdirSync(join(dir, "docs"));
    for (const name of ["vite.config.ts", "svelte.config.ts", "eslint.config.mjs", "notes.ts"]) {
      writeFileSync(join(dir, name), "");
    }
    assert.deepEqual(defaultRoots(dir), [join(dir, "src"), join(dir, "svelte.config.ts"), join(dir, "vite.config.ts")]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("collectFiles walks directories and skips node_modules", () => {
  const dir = mkdtempSync(join(tmpdir(), "tscheck-"));
  try {
    mkdirSync(join(dir, "node_modules"));
    for (const name of ["a.ts", "b.svelte", "c.js", "node_modules/d.ts"]) writeFileSync(join(dir, name), "");
    assert.deepEqual(collectFiles([dir]), [join(dir, "a.ts"), join(dir, "b.svelte")]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("main reports violations and returns a nonzero code, or stays silent when clean", () => {
  const dir = mkdtempSync(join(tmpdir(), "tscheck-"));
  const originalError = console.error;
  const diagnostics: string[] = [];
  try {
    writeFileSync(join(dir, "bad.ts"), "// @ts-ignore\nfoo();");
    writeFileSync(join(dir, "good.ts"), "let a: unknown;");
    console.error = (message: unknown) => { diagnostics.push(String(message)); };
    assert.equal(main([join(dir, "bad.ts")]), 1);
    assert.match(diagnostics.join("\n"), /no-ts-suppression/);
    assert.match(diagnostics.at(-1) ?? "", /1 TypeScript policy violation/);
    diagnostics.length = 0;
    assert.equal(main([join(dir, "good.ts")]), 0);
    assert.deepEqual(diagnostics, []);
  } finally {
    console.error = originalError;
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the checker and its tests pass their own policy", () => {
  const here = fileURLToPath(new URL(".", import.meta.url));
  assert.deepEqual(collectFiles([here]).flatMap((file) => checkSource(readFileSync(file, "utf8"), file)), []);
});

test("checks Svelte generic defaults and constraints", () => {
  for (const generics of ["T = any", "T extends any", "T extends Array<any> = string[]"]) {
    const source = `<script lang="ts" generics="${generics}">let value: T;</script>`;
    const violations = checkSvelte(source, "Generic.svelte");
    assert.equal(violations.length, 1);
    assert.equal(violations[0]?.rule, "no-explicit-any");
    assert.equal(violations[0]?.column, source.indexOf("any") + 1);
  }
});
