import { readFile, realpath, mkdir } from 'node:fs/promises';
import { resolve, sep, extname, join } from 'node:path';
import { tmpdir } from 'node:os';

const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const config = JSON.parse(Buffer.concat(chunks).toString());
const { chromium } = await import(config.Module);
const browser = await chromium.connectOverCDP(config.Endpoint);
const origin = 'http://127.0.0.1:7457';
const web = await realpath(config.Web);
const reads = new Set(config.ReadMethods);
// A short list of the kinds of request this tab blocks, never the requests
// themselves: a step that times out needs to know what cannot work here, and
// no URL, header or cookie may enter the list or its report.
const blockedLimit = 20;
const blocked = new Map();
let blockedDropped = false;
const noteBlocked = kind => {
  if (!blocked.has(kind) && blocked.size === blockedLimit) {
    blockedDropped = true;
    return;
  }
  blocked.set(kind, (blocked.get(kind) || 0) + 1);
};
const blockedSummary = () => {
  if (blocked.size === 0) return 'QA blocked nothing yet.';
  const kinds = [...blocked].map(([kind, count]) => count > 1 ? kind + ' x' + count : kind);
  const full = blockedDropped ? ', and further kinds' : '';
  return 'QA blocked kinds: ' + kinds.join(', ') + full;
};
// Page errors and failed assets, as kinds with counts. The kinds form a short
// fixed set, so the list needs no limit, and it never holds a URL or a message.
const failed = new Map();
const noteFailure = kind => failed.set(kind, (failed.get(kind) || 0) + 1);
const failureSummary = () => {
  if (failed.size === 0) return 'QA page errors and failed assets: none recorded.';
  const kinds = [...failed].map(([kind, count]) => count > 1 ? kind + ' x' + count : kind);
  return 'QA page errors and failed assets: ' + kinds.join(', ');
};
const requestKind = url => url.origin !== origin ? 'external request' :
  url.pathname.startsWith('/api/') ? 'API request' : 'asset request';
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
  '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.mjs': 'text/javascript',
};
const deny = route => {
  const request = route.request();
  const kind = requestKind(new URL(request.url()));
  // Do not print URLs, arguments, headers, or cookies: they may hold credentials.
  noteBlocked(request.method() + ' ' + kind);
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
await context.routeWebSocket('**/*', socket => {
  noteBlocked('socket');
  socket.close();
});
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
  const isIndex = ['/', '/index.html', '/connect'].includes(url.pathname);
  try {
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
    // The reason stays in the report, so a blank tab names its failed load.
    noteFailure(isIndex ? 'engine index unavailable' : 'asset unavailable');
    await route.fulfill({ status: 404, body: 'QA asset unavailable' });
  }
});
const page = await context.newPage();
await page.addInitScript(() => { globalThis.__dynaReadOnlyQA = true; });
page.on('pageerror', () => noteFailure('page error'));
page.on('console', message => {
  if (message.type() === 'error') noteFailure('console error');
});
page.on('requestfailed', request => noteFailure(request.resourceType() + ' request failed'));
page.on('response', response => {
  const status = response.status();
  if (status >= 400) noteFailure('HTTP ' + status + ' ' + requestKind(new URL(response.url())));
});
// A timed-out step has no other way to learn why. The page exposes one call
// that names every blocked kind and saves a picture, so an agent can tell a
// restriction from a branch bug. It holds kinds only: no URL, header or
// cookie ever enters the list or its answer.
const reportDir = join(process.env.DYNA_SCRATCH || tmpdir(), 'qa-report-' + process.pid);
const reportPage = join(reportDir, 'page.png');
await mkdir(reportDir, { recursive: true, mode: 0o700 });
const report = async () => {
  console.log(blockedSummary());
  console.log(failureSummary());
  let screenshot = null;
  try {
    await page.screenshot({ path: reportPage });
    screenshot = reportPage;
    console.log('QA page screenshot: ' + reportPage);
  } catch {
    console.log('QA page screenshot unavailable: the tab did not draw.');
  }
  return {
    blocked: [...blocked].map(([kind, count]) => ({ kind, count })),
    failed: [...failed].map(([kind, count]) => ({ kind, count })),
    screenshot,
  };
};
await page.exposeFunction('dynaQAReport', report);
let stopped;
const done = new Promise(resolve => { stopped = resolve; });
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, stopped);
page.once('close', stopped);
browser.once('disconnected', stopped);
try {
  await page.goto(origin, { waitUntil: 'domcontentloaded' });
  const cdp = await context.newCDPSession(page);
  const { targetInfo } = await cdp.send('Target.getTargetInfo');
  await cdp.detach();
  const pageSocket = new URL(config.Endpoint);
  pageSocket.pathname = '/devtools/page/' + targetInfo.targetId;
  pageSocket.search = '';
  pageSocket.hash = '';
  console.log('QA page CDP target: ' + targetInfo.targetId);
  console.log('QA page CDP websocket: ' + pageSocket.href);
  console.log('Read-only QA tab ready at ' + origin + '.');
  console.log('Read-only QA cannot show settings writes, live streams that need a WebSocket (the computer viewer and voice calls), or server fields newer than the installed engine.');
  console.log('The computer stream stays closed in this tab: its socket relays desktop input, so QA is not read-only with it open. Check the viewer with dyna browser against the app the engine serves.');
  console.log('When a step times out, call globalThis.dynaQAReport() in the QA page: it lists every blocked request kind and every page error or failed asset with its count, and saves a screenshot to the path it returns.');
  if (config.Steps) {
    for (const [index, step] of config.Steps.entries()) {
      try {
        if (step.viewport) {
          await page.setViewportSize(step.viewport);
        } else if (step.goto !== undefined) {
          const url = new URL(step.goto, origin);
          if (url.origin !== origin) throw new Error('goto must stay on ' + origin);
          await page.goto(url.href, { waitUntil: 'domcontentloaded' });
        } else if (step.click !== undefined) {
          await page.locator(step.click).click();
        } else if (step.type) {
          await page.locator(step.type.selector).pressSequentially(step.type.text);
        } else if (step.waitFor !== undefined) {
          await page.locator(step.waitFor).waitFor();
        } else if (step.screenshot !== undefined) {
          await page.screenshot({ path: step.screenshot });
          console.log('QA page screenshot: ' + step.screenshot);
        }
      } catch (error) {
        await report();
        throw new Error('QA step ' + (index + 1) + ' failed: ' + error.message, { cause: error });
      }
    }
    console.log('QA steps complete.');
  } else {
    console.log('Keep this command running; press Ctrl+C to close the QA tab.');
    await done;
  }
} finally {
  // Ignore route callbacks that fail while their context is shutting down.
  await context.unrouteAll({ behavior: 'ignoreErrors' }).catch(() => {});
  await context.close().catch(() => {});
  await browser.close().catch(() => {});
}
