export function nameProblem(name: string, taken: Iterable<string>): string | undefined {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(name)) return "Use 1 to 64 letters, digits, dots, underscores or hyphens, starting with a letter or digit.";
  if (["root", "parent", "children", "siblings"].includes(name)) return "This name is reserved.";
  if ([...taken].includes(name)) return "This name is already in use.";
  return undefined;
}
