import { themeFor, type Theme } from "./themes.ts";

const key = "dyna.theme";

function apply(next: Theme) {
  const root = document.documentElement;
  for (const [name, value] of Object.entries(next.tokens)) root.style.setProperty(name, value);
  for (const [name, value] of Object.entries({
    "--code-keyword": next.terminal.ansi[5], "--code-string": next.terminal.ansi[2],
    "--code-number": next.terminal.ansi[3], "--code-title": next.terminal.ansi[4],
    "--code-comment": next.tokens["--muted"],
  })) root.style.setProperty(name, value);
  root.style.colorScheme = next.mode;
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", next.tokens["--bg"]);
}

// The first read runs before the first paint: index.html's main script holds rendering until this module has run.
const first = themeFor(() => localStorage.getItem(key));
apply(first);
let current = $state.raw(first);
// A theme stored by another tab applies here too.
window.addEventListener("storage", event => {
  if (event.key !== key) return;
  current = themeFor(() => event.newValue);
  apply(current);
});

export const theme = () => current;
// The owner's pick applies at once and stays on this browser. A browser that blocks site storage keeps it for this tab only.
export function choose(next: Theme) {
  current = next;
  apply(next);
  try { localStorage.setItem(key, next.id); } catch { /* Storage is blocked: the pick lasts until the tab closes. */ }
}
