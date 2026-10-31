import { test } from "node:test";
import assert from "node:assert/strict";
import { globSync, readFileSync } from "node:fs";
import { carbon, themeFor, themes } from "./themes.ts";

interface Color { r: number; g: number; b: number; a: number }
function parse(value: string): Color {
  if (value.startsWith("#")) {
    const n = parseInt(value.slice(1), 16);
    return { r: n >> 16 & 255, g: n >> 8 & 255, b: n & 255, a: 1 };
  }
  const [r, g, b, a] = value.match(/[\d.]+/g)!.map(Number);
  return { r, g, b, a: a / 100 };
}
const over = (top: Color, under: Color): Color => {
  const mix = (t: number, u: number) => t * top.a + u * (1 - top.a);
  return { r: mix(top.r, under.r), g: mix(top.g, under.g), b: mix(top.b, under.b), a: 1 };
};
const luminance = ({ r, g, b }: Color) => {
  const channel = (c: number) => c / 255 <= 0.04045 ? c / 255 / 12.92 : ((c / 255 + 0.055) / 1.055) ** 2.4;
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
};
const contrast = (a: Color, b: Color) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

test("every theme keeps the contrast floors over each surface", () => {
  const floors = [["--text", 11], ["--muted", 5], ["--attention", 4.5], ["--danger", 4.5], ["--success", 4.5], ["--focus", 3]] as const;
  for (const { id, tokens } of themes) {
    const page = parse(tokens["--bg"]);
    for (const surface of ["--bg", "--sidebar", "--raised"] as const) {
      const under = parse(tokens[surface]);
      for (const [token, floor] of floors) {
        const ratio = contrast(parse(tokens[token]), under);
        assert.ok(ratio >= floor, `${id}: ${token} on ${surface} is ${ratio.toFixed(2)}, under ${floor}`);
      }
    }
    assert.ok(contrast(page, parse(tokens["--focus"])) >= 5.5, `${id}: the label on --focus`);
    const edge = contrast(over(parse(tokens["--line"]), parse(tokens["--raised"])), page);
    assert.ok(edge >= 1.19, `${id}: the card edge is ${edge.toFixed(2)} against the page`);
  }
});

test("an unknown stored id falls back to Carbon, and a known id wins", () => {
  assert.equal(themeFor(null), carbon);
  assert.equal(themeFor("sepia"), carbon);
  assert.equal(themeFor("light").id, "light");
});

test("no style in the app holds a raw color", () => {
  const raw = /#[0-9a-f]{3,8}\b|\b(?:rgb|rgba|hsl|hsla|hwb|lab|lch|oklab|oklch)\(\s*[\d.]|:\s*(?:black|white)\b/gi;
  const named = /(?<![-\w.#])(?:black|white|red|green|blue|yellow|orange|purple|pink|brown|gray|grey)(?![-\w])/gi;
  const found = globSync("src/**/*.{svelte,css}", { cwd: new URL("../..", import.meta.url).pathname }).flatMap(file => {
    const text = readFileSync(new URL(`../../${file}`, import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\/|<!--[\s\S]*?-->/g, "");
    const styles = file.endsWith(".css") ? text : text.match(/<style[\s\S]*?<\/style>/g)?.join("\n") ?? "";
    return [...text.match(raw) ?? [], ...styles.match(named) ?? []].map(hit => `${file}: ${hit}`);
  });
  assert.deepEqual(found, []);
});
