import { readFile, realpath } from 'node:fs/promises';
import { resolve, sep, extname } from 'node:path';

const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const config = JSON.parse(Buffer.concat(chunks).toString());
const { chromium } = await import(config.Module);
const browser = await chromium.connectOverCDP(config.Endpoint);
const origin = 'http://127.0.0.1:7457';
const web = await realpath(config.Web);
const reads = new Set(config.ReadMethods);
const source = browser.contexts()[0];
// The QA tab starts as a copy of the agent's paired tab: the same cookies and
// the same stored device state. A tab without that state is unpaired, and the
// traced page then measures the pairing screen instead of the branch.
const context = await browser.newContext({
  serviceWorkers: 'block',
  storageState: await source.storageState(),
});
const types = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript',
  '.css': 'text/css', '.svg': 'image/svg+xml', '.json': 'application/json',
  '.webmanifest': 'application/manifest+json', '.wasm': 'application/wasm',
  '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2',
};
const deny = route => {
  const request = route.request();
  const url = new URL(request.url());
  const kind = url.origin !== origin ? 'external request' :
    url.pathname.startsWith('/api/') ? 'API request' : 'asset request';
  // Do not print URLs, arguments, headers, or cookies: they may hold credentials.
  console.log('QA restriction: blocked ' + request.method() + ' ' + kind);
  return route.fulfill({
    status: 403, contentType: 'application/json',
    body: JSON.stringify({ error: 'read-only QA mode' }),
  });
};
// Every socket stays shut. The computer view socket relays browser bytes to
// wayvnc, which accepts RFB input, so an open viewer is not read-only, and
// the tab cannot show the stream. Check the viewer with dyna browser against
// the app the engine serves.
await context.routeWebSocket('**/*', socket => socket.close());
// A computer's last picture is a read: the Computers page is made of it, and
// the viewer shows it while its stream connects.
const computerShot = name => /^computers\/[A-Za-z0-9][A-Za-z0-9_.-]{0,95}\/shot\.png$/.test(name);
await context.route('**/*', async route => {
  const request = route.request();
  const url = new URL(request.url());
  const method = request.method();
  if (url.origin !== origin) return deny(route);
  if (url.pathname.startsWith('/api/')) {
    const name = url.pathname.slice(5);
    if ((method === 'POST' && reads.has(name)) ||
        (method === 'GET' && (name === 'events' || name === 'events/poll' || computerShot(name)))) {
      // A later dyna browser call renews the device cookie in the source context.
      await context.addCookies(await source.cookies(origin));
      return route.continue();
    }
    return deny(route);
  }
  if (url.pathname.startsWith('/attachments/')) {
    return method === 'GET' ? route.continue() : deny(route);
  }
  if (method !== 'GET' && method !== 'HEAD') return deny(route);
  try {
    const isIndex = ['/', '/index.html', '/connect'].includes(url.pathname);
    const relative = isIndex ? 'index.html' : decodeURIComponent(url.pathname).slice(1);
    const file = await realpath(resolve(web, relative));
    if (!file.startsWith(web + sep)) return deny(route);
    let body = await readFile(file);
    let headers = { 'content-type': types[extname(file)] || 'application/octet-stream',
      'cache-control': 'no-store' };
    if (isIndex) {
      // Use the engine's actual metadata and security headers at its unchanged origin.
      const response = await route.fetch({ method: 'GET' });
      const live = await response.text();
      const meta = live.match(/<meta name="dyna-web" content="[^"]*">/);
      if (!response.ok() || !meta) throw new Error('Engine page metadata is unavailable');
      body = Buffer.from(body.toString().replace('</head>', meta[0] + '\n</head>'));
      headers = { ...response.headers(), ...headers };
      delete headers['content-length'];
      delete headers['content-encoding'];
    }
    await route.fulfill({ status: 200, headers, body: method === 'HEAD' ? Buffer.alloc(0) : body });
  } catch {
    await route.fulfill({ status: 404, body: 'QA asset unavailable' });
  }
});
const page = await context.newPage();
await page.addInitScript(() => { globalThis.__dynaReadOnlyQA = true; });
let stopped;
const done = new Promise(resolve => { stopped = resolve; });
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, stopped);
page.once('close', stopped);
browser.once('disconnected', stopped);
try {
  await page.goto(origin, { waitUntil: 'domcontentloaded' });
  console.log('Read-only QA tab ready at ' + origin + '. Keep this command running; press Ctrl+C to close it.');
  console.log('The computer stream stays closed in this tab: its socket relays desktop input, so QA is not read-only with it open. Check the viewer with dyna browser against the app the engine serves.');
  await done;
} finally {
  // Ignore route callbacks that fail while their context is shutting down.
  await context.unrouteAll({ behavior: 'ignoreErrors' }).catch(() => {});
  await context.close().catch(() => {});
  await browser.close().catch(() => {});
}
