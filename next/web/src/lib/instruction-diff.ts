export type DiffLine = { kind: "same" | "add" | "remove"; text: string; line: number };

export type DiffHunk = { line: number; lines: DiffLine[] };

// Keep unchanged lines between changes so the review gives each change context.
export function instructionDiff(before: string, after: string): DiffLine[] {
  const a = before ? before.split("\n") : [], b = after ? after.split("\n") : [];
  const rows = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1));
  for (let i = a.length - 1; i >= 0; i--)
    for (let j = b.length - 1; j >= 0; j--)
      rows[i][j] = a[i] === b[j] ? rows[i + 1][j + 1] + 1 : Math.max(rows[i + 1][j], rows[i][j + 1]);
  const out: DiffLine[] = [];
  let i = 0, j = 0;
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      out.push({ kind: "same", text: a[i++], line: ++j });
    } else if (i < a.length && (j === b.length || rows[i + 1][j] >= rows[i][j + 1])) {
      out.push({ kind: "remove", text: a[i++], line: j + 1 });
    } else {
      out.push({ kind: "add", text: b[j++], line: j });
    }
  }
  return out;
}

export function instructionHunks(lines: DiffLine[]): DiffHunk[] {
  const ranges: { start: number; end: number; line: number }[] = [];
  for (let i = 0; i < lines.length;) {
    if (lines[i].kind === "same") { i++; continue; }
    const first = i;
    while (i < lines.length && lines[i].kind !== "same") i++;
    let start = first - 1, end = i;
    while (start >= 0 && lines[start].kind === "same" && !lines[start].text.trim()) start--;
    while (end < lines.length && lines[end].kind === "same" && !lines[end].text.trim()) end++;
    if (start < 0 || lines[start].kind !== "same") start = first;
    if (end >= lines.length || lines[end].kind !== "same") end = i - 1;
    const previous = ranges.at(-1);
    if (previous && start <= previous.end) previous.end = end;
    else ranges.push({ start, end, line: lines[first].line });
  }
  return ranges.map(({ start, end, line }) => ({ line, lines: lines.slice(start, end + 1).filter(item => item.kind !== "same" || item.text.trim()) }));
}
