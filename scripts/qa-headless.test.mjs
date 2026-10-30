// Checks the headless QA runner against a fake browser. No Chromium starts, so
// the tests stay fast: they cover config checks, the device cookie, the
// external request block, and the browser close after a failed step.
import assert from 'node:assert/strict';
import test from 'node:test';
import { parseConfig, runQA } from './qa-headless.mjs';

const token = 'device-token-value';
const origin = 'http://127.0.0.1:7457';
const config = (overrides = {}) => JSON.stringify({
  Module: '/modules/playwright-core/index.mjs', Chromium: '/usr/bin/chromium',
  Origin: origin, Cookie: token, Steps: [], ...overrides,
});

// fakeChromium records what runQA asks of the browser, its context and its page.
function fakeChromium() {
  const seen = { launch: undefined, cookies: [], handlers: [], gotos: [], closed: false };
  const page = {
    setViewportSize: async () => {},
    goto: async url => { seen.gotos.push(url); },
    locator: () => ({ click: async () => {}, pressSequentially: async () => {}, waitFor: async () => {} }),
    screenshot: async () => {},
  };
  const context = {
    addCookies: async cookies => { seen.cookies.push(...cookies); },
    route: async (_pattern, handler) => { seen.handlers.push(handler); },
    routeWebSocket: async () => {},
    newPage: async () => page,
  };
  const browser = {
    newContext: async options => { seen.context = options; return context; },
    close: async () => { seen.closed = true; },
  };
  return {
    seen,
    chromium: { launch: async options => { seen.launch = options; return browser; } },
  };
}

// fakeRoute reports the one decision the restriction makes for a request.
function fakeRoute(url, method, outcomes) {
  return {
    request: () => ({ url: () => url, method: () => method }),
    abort: reason => outcomes.push('abort ' + reason),
    continue: () => outcomes.push('continue'),
  };
}

test('parseConfig rejects an Origin that is not a web address', () => {
  assert.throws(() => parseConfig(config({ Origin: 'file:///tmp/app' })), /Origin must use http or https/);
});

test('parseConfig keeps the device cookie out of its JSON error', () => {
  const truncated = '{"Cookie": "' + token + '",';
  assert.throws(() => parseConfig(truncated), error => {
    assert.equal(error.message, 'QA config is not valid JSON');
    return true;
  });
});

test('runQA launches headless Chromium and adds the device cookie without logging it', async () => {
  const fake = fakeChromium();
  const lines = [];
  await runQA(parseConfig(config({ Steps: [{ goto: '/' }] })), fake.chromium, line => lines.push(line));
  assert.equal(fake.seen.launch.executablePath, '/usr/bin/chromium');
  assert.equal(fake.seen.launch.headless, true);
  assert.equal(fake.seen.context.reducedMotion, 'reduce');
  assert.deepEqual(fake.seen.cookies, [{ name: 'dyna_device', value: token, url: origin }]);
  assert.equal(lines.join('\n').includes(token), false);
  assert.equal(fake.seen.closed, true);
});

test('runQA lets Origin requests through, including its API, and aborts the rest', async () => {
  const fake = fakeChromium();
  await runQA(parseConfig(config()), fake.chromium, () => {});
  const [restrict] = fake.seen.handlers;
  const outcomes = [];
  await restrict(fakeRoute('https://example.com/app.js', 'GET', outcomes));
  await restrict(fakeRoute(origin + '/api/devices', 'POST', outcomes));
  await restrict(fakeRoute(origin + '/app.js', 'GET', outcomes));
  assert.deepEqual(outcomes, ['abort blockedbyclient', 'continue', 'continue']);
});

test('runQA closes the browser when a goto leaves Origin', async () => {
  const fake = fakeChromium();
  const run = runQA(parseConfig(config({ Steps: [{ goto: 'https://example.com/' }] })), fake.chromium, () => {});
  await assert.rejects(run, /goto must stay on http:\/\/127\.0\.0\.1:7457/);
  assert.equal(fake.seen.closed, true);
});
