export type DiffLine = { kind: "same" | "add" | "remove"; text: string; line: number };

export type DiffHunk = { line: number; lines: DiffLine[] };

// Keep unchanged lines between changes so the review gives each change context.
export function instructionDiff(before: string, after: string): DiffLine[] {
  const a = before ? before.split("\n") : [], b = after ? after.split("\n") : [];
  let start = 0, aEnd = a.length, bEnd = b.length;
  while (start < aEnd && start < bEnd && a[start] === b[start]) start++;
  while (aEnd > start && bEnd > start && a[aEnd - 1] === b[bEnd - 1]) { aEnd--; bEnd--; }

  const out: DiffLine[] = [];
  for (let n = 0; n < start; n++) out.push({ kind: "same", text: a[n], line: n + 1 });

  const height = aEnd - start, width = bEnd - start;
  // Bound both allocation and work on unrelated large documents. The fallback
  // still shows all changed lines and preserves their target line positions.
  const rows = (height + 1) * (width + 1) <= 1_000_000 && height && width
    ? Array.from({ length: height + 1 }, () => new Uint32Array(width + 1))
    : null;
  if (rows) {
    for (let x = height - 1; x >= 0; x--)
      for (let y = width - 1; y >= 0; y--)
        rows[x][y] = a[start + x] === b[start + y] ? rows[x + 1][y + 1] + 1 : Math.max(rows[x + 1][y], rows[x][y + 1]);
  }
  let i = start, j = start;
  while (i < aEnd || j < bEnd) {
    if (i < aEnd && j < bEnd && a[i] === b[j]) {
      out.push({ kind: "same", text: a[i++], line: ++j });
    } else if (i < aEnd && (j === bEnd || !rows || rows[i - start + 1][j - start] >= rows[i - start][j - start + 1])) {
      out.push({ kind: "remove", text: a[i++], line: j + 1 });
    } else {
      out.push({ kind: "add", text: b[j++], line: j });
    }
  }
  while (i < a.length) out.push({ kind: "same", text: a[i++], line: ++j });
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
