// A stable hue for a session name: an FNV-1a hash picks one of 24 hues 15 degrees apart,
// so a session keeps its color on every page and similar names still differ.
export function hue(name: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) hash = Math.imul(hash ^ name.charCodeAt(i), 0x01000193);
  return ((hash >>> 0) % 24) * 15;
}

const words = (name: string) => name.split(/[\s_.-]+/).filter(Boolean);

// Up to two initials: "lead-web" gives "LW", "root" gives "RO".
export function initials(name: string): string {
  const parts = words(name);
  return (parts.length > 1 ? parts[0][0] + parts[1][0] : name.slice(0, 2)).toUpperCase();
}

// Initials that tell names apart, the rooms on the rail or the sessions in one room. First each name, in name order,
// takes its two words' first letters while they are free, so a fallback never takes another name's own initials. Each
// name left then takes the first free of its first two letters, its first letter and a digit of its own (the last
// first), then its first letter and a counting digit. "design-system" and "docs-site" give "DS" and "DO", and
// "reviewer-2", "reviewer-7" and "reviewer-22" give "R2", "R7" and "RE"; the order makes every page agree.
export function distinctInitials(names: string[]): Map<string, string> {
  const sorted = [...new Set(names)].sort((a, b) => a.localeCompare(b));
  const result = new Map<string, string>(), taken = new Set<string>();
  for (const name of sorted) if (!taken.has(initials(name))) { taken.add(initials(name)); result.set(name, initials(name)); }
  for (const name of sorted.filter(name => !result.has(name))) {
    const first = (words(name)[0] ?? name).charAt(0).toUpperCase();
    const own = [...name.match(/\d/g) ?? []].reverse();
    const label = [name.slice(0, 2).toUpperCase(), ...[...own, ..."123456789"].map(digit => first + digit)]
      .find(candidate => !taken.has(candidate)) ?? initials(name);
    taken.add(label);
    result.set(name, label);
  }
  return result;
}
