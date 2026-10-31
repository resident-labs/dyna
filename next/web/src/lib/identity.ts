// A stable hue for a session name: an FNV-1a hash picks one of 24 hues 15 degrees apart,
// so a session keeps its color on every page and similar names still differ.
export function hue(name: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) hash = Math.imul(hash ^ name.charCodeAt(i), 0x01000193);
  return ((hash >>> 0) % 24) * 15;
}

// Up to two initials: "lead-web" gives "LW", "root" gives "RO".
export function initials(name: string): string {
  const words = name.split(/[\s_.-]+/).filter(Boolean);
  return (words.length > 1 ? words[0][0] + words[1][0] : name.slice(0, 2)).toUpperCase();
}
