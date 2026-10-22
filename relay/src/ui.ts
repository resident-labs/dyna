// The relay's pages: one static HTML shell per path, one script and one
// style sheet. The script reads the URL and calls the JSON API. Nothing
// here takes HTML from the server: every value goes in through
// textContent.

const titles: Record<string, string> = {
  "/": "Resident Labs Connect",
  "/signin": "Sign in · Resident Labs Connect",
  "/enroll": "Join · Resident Labs Connect",
  "/recover": "Recover · Resident Labs Connect",
  "/link": "Link a machine · Resident Labs Connect",
  "/open": "Open a machine · Resident Labs Connect",
};

export function page(path: string): string {
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>${titles[path] ?? titles["/"]}</title>
<link rel="stylesheet" href="/app.css">
<script type="module" src="/app.js"></script>
</head>
<body>
<header><span class="logo">◆</span> Resident Labs <b>Connect</b></header>
<main id="app" data-page="${path}"><p class="muted">Loading…</p></main>
</body>
</html>`;
}

export const style = `
:root { --bg:#0f1115; --card:#181b22; --line:#2a2f3a; --text:#e8eaf0; --muted:#9aa3b5; --accent:#7aa2ff; --bad:#ff7a7a; --ok:#7adf9a; }
@media (prefers-color-scheme: light) { :root { --bg:#f6f7f9; --card:#fff; --line:#dde1e8; --text:#161a22; --muted:#5b6475; --accent:#2f5fe0; --bad:#c62828; --ok:#1e7d3c; } }
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--text); font:15px/1.5 system-ui, -apple-system, Segoe UI, sans-serif; }
header { padding:14px 20px; border-bottom:1px solid var(--line); font-size:16px; }
header .logo { color:var(--accent); }
main { max-width:760px; margin:0 auto; padding:20px; }
h1 { font-size:22px; margin:8px 0 16px; }
h2 { font-size:16px; margin:0 0 10px; }
section { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:16px; margin:0 0 16px; }
.row { display:flex; gap:10px; align-items:center; justify-content:space-between; padding:8px 0; border-top:1px solid var(--line); flex-wrap:wrap; }
.row:first-of-type { border-top:0; }
.muted { color:var(--muted); }
.small { font-size:13px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.big { font-size:24px; letter-spacing:2px; }
button { font:inherit; border-radius:8px; border:1px solid var(--line); background:transparent; color:var(--text); padding:8px 14px; cursor:pointer; }
button.primary { background:var(--accent); border-color:var(--accent); color:#fff; }
button.danger { color:var(--bad); }
button:disabled { opacity:.5; cursor:default; }
input, select { font:inherit; padding:8px 10px; border-radius:8px; border:1px solid var(--line); background:var(--bg); color:var(--text); }
.error { color:var(--bad); }
.ok { color:var(--ok); }
.codes { display:grid; grid-template-columns:repeat(2, 1fr); gap:6px; }
.actions { display:flex; gap:8px; flex-wrap:wrap; margin-top:12px; }
.pill { font-size:12px; border:1px solid var(--line); border-radius:99px; padding:1px 8px; color:var(--muted); }
`;

export { default as script } from "../browser/app.js";
