// Runs branch QA in its own headless Chromium. It reads one JSON config on
// stdin, opens a fresh context with the temporary engine device cookie, and
// runs the same viewport, goto, click, type, waitFor and screenshot steps as
// scripts/qa-branch.mjs. Requests reach only Origin, and the browser closes
// when the steps finish or fail.
import { pathToFileURL } from 'node:url';

const cookieName = 'dyna_device';

const isRecord = value => typeof value === 'object' && value !== null && !Array.isArray(value);
const nonEmpty = (value, name) => {
  if (typeof value !== 'string' || value === '') throw new Error('QA config needs a non-empty ' + name);
  return value;
};
const isCount = value => Number.isInteger(value) && value > 0;

const parseOrigin = value => {
  const url = new URL(nonEmpty(value, 'Origin'));
  if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new Error('QA Origin must use http or https');
  return url.origin;
};

const parseStep = (step, index) => {
  const label = 'QA step ' + (index + 1);
  const kinds = isRecord(step) ? Object.keys(step) : [];
  if (kinds.length !== 1) throw new Error(label + ' needs exactly one action');
  const [kind] = kinds;
  const value = step[kind];
  switch (kind) {
    case 'viewport':
      if (!isRecord(value) || !isCount(value.width) || !isCount(value.height)) throw new Error(label + ' viewport needs width and height');
      return { kind, value: { width: value.width, height: value.height } };
    case 'goto':
    case 'click':
    case 'waitFor':
    case 'screenshot':
      if (typeof value !== 'string' || value === '') throw new Error(label + ' ' + kind + ' needs a string');
      return { kind, value };
    case 'type':
      if (!isRecord(value) || typeof value.selector !== 'string' || typeof value.text !== 'string') {
        throw new Error(label + ' type needs selector and text');
      }
      return { kind, value: { selector: value.selector, text: value.text } };
    default:
      throw new Error(label + ' has an unknown action');
  }
};

// parseConfig validates stdin once. Later code receives only typed values.
export function parseConfig(text) {
  let raw;
  try {
    raw = JSON.parse(text);
  } catch {
    // The parser message can quote its input, and the input holds the device cookie.
    throw new Error('QA config is not valid JSON');
  }
  if (!isRecord(raw)) throw new Error('QA config must be a JSON object');
  if (!Array.isArray(raw.Steps)) throw new Error('QA config needs a Steps list');
  return {
    module: nonEmpty(raw.Module, 'Module'),
    executable: nonEmpty(raw.Chromium, 'Chromium'),
    origin: parseOrigin(raw.Origin),
    cookie: nonEmpty(raw.Cookie, 'Cookie'),
    steps: raw.Steps.map(parseStep),
  };
}

// Requests reach only Origin, including its API. Every other request fails inside the browser.
const restrict = (route, config) => {
  const url = new URL(route.request().url());
  return url.origin === config.origin ? route.continue() : route.abort('blockedbyclient');
};

const runStep = async (page, step, origin, log) => {
  switch (step.kind) {
    case 'viewport':
      return page.setViewportSize(step.value);
    case 'goto': {
      const url = new URL(step.value, origin);
      if (url.origin !== origin) throw new Error('goto must stay on ' + origin);
      return page.goto(url.href, { waitUntil: 'domcontentloaded' });
    }
    case 'click':
      return page.locator(step.value).click();
    case 'type':
      return page.locator(step.value.selector).pressSequentially(step.value.text);
    case 'waitFor':
      return page.locator(step.value).waitFor();
    case 'screenshot':
      await page.screenshot({ path: step.value });
      log('QA page screenshot: ' + step.value);
      return undefined;
  }
};

// runQA runs the steps in one fresh browser and always closes it.
export async function runQA(config, chromium, log = console.log) {
  const browser = await chromium.launch({
    executablePath: config.executable,
    headless: true,
    // Only the Origin host resolves. Other names fail before a request leaves Chromium.
    args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE ' + new URL(config.origin).hostname],
  });
  try {
    const context = await browser.newContext({ serviceWorkers: 'block', acceptDownloads: false });
    await context.addCookies([{ name: cookieName, value: config.cookie, url: config.origin }]);
    await context.route('**/*', route => restrict(route, config));
    // Sockets relay live streams, such as the computer viewer, so every socket stays shut.
    await context.routeWebSocket('**/*', socket => socket.close());
    const page = await context.newPage();
    await page.goto(config.origin, { waitUntil: 'domcontentloaded' });
    log('Read-only QA tab ready at ' + config.origin + '.');
    for (const [index, step] of config.steps.entries()) {
      try {
        await runStep(page, step, config.origin, log);
      } catch (error) {
        throw new Error('QA step ' + (index + 1) + ' failed: ' + error.message, { cause: error });
      }
    }
    log('QA steps complete.');
  } finally {
    await browser.close().catch(() => {});
  }
}

const main = async () => {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  const config = parseConfig(Buffer.concat(chunks).toString());
  const { chromium } = await import(pathToFileURL(config.module).href);
  await runQA(config, chromium);
};

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
