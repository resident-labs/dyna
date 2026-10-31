export type DiffLine = { kind: "same" | "add" | "remove"; text: string; line: number };

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
