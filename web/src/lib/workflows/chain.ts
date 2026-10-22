// The phase chain wraps onto several lines. A connector only joins two pills on
// the same line, so one that crosses a wrap is hidden: a wrapped line must not
// end or start with a line pointing at nothing.
export function hiddenConnectors(tops: number[]): number[] {
  const hidden: number[] = []
  for (let i = 0; i + 1 < tops.length; i++) if (tops[i] !== tops[i + 1]) hidden.push(i)
  return hidden
}
