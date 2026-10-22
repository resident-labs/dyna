import assert from 'node:assert/strict';

const origin = 'http://127.0.0.1:7457';

let handler;
let closed;
let socketsBlocked = false;
let removingRoutes = false;
let pendingRoute;
const cookieStarted = Promise.withResolvers();
const cookieFinished = Promise.withResolvers();
let closeWithPendingCookie = false;
const context = {
  async addCookies(cookies) {
    assert.equal(cookies[0].name, 'paired');
    if (closeWithPendingCookie) {
      cookieStarted.resolve();
      await cookieFinished.promise;
    }
  },
  async unrouteAll(options) {
    assert.deepEqual(options, { behavior: 'ignoreErrors' });
    removingRoutes = true;
    const ignored = pendingRoute.catch(error => {
      assert.equal(error.message, 'TargetClosedError: browserContext.addCookies');
      console.log('QA teardown ignored the pending cookie error');
    });
    cookieFinished.reject(new Error('TargetClosedError: browserContext.addCookies'));
    await ignored;
  },
  async routeWebSocket(pattern, callback) {
    assert.equal(pattern, '**/*');
    callback({ close() { socketsBlocked = true; } });
  },
  async route(pattern, callback) { assert.equal(pattern, '**/*'); handler = callback; },
  async newPage() {
    return {
      async addInitScript() {},
      once(event, callback) { assert.equal(event, 'close'); closed = callback; },
      async goto(origin) {
        assert.equal(origin, 'http://127.0.0.1:7457');
        const checks = [
          ['/api/Rooms', 'POST', 'continue'],
          ['/api/Rooms', 'GET', 403],
          ['/api/Send', 'POST', 403],
          ['/api/ConfigCheckProfile', 'POST', 403],
          ['/api/AccountUsage', 'POST', 403],
          ['/api/ProfileModels', 'POST', 403],
          ['/api/UpdateNotice', 'POST', 403],
          ['/api/UnknownFutureMethod', 'POST', 403],
          ['/api/logout', 'POST', 403],
          ['/api/pair', 'POST', 403],
          ['/api/terminal/attach', 'POST', 403],
          ['/api/computers/x/control', 'POST', 403],
          ['/api/computers/x/shot.png', 'GET', 'continue'],
          ['/api/computers/../evil/shot.png', 'GET', 403],
          ['http://evil.test/api/computers/x/shot.png', 'GET', 403],
          ['/api/events', 'GET', 'continue'],
          ['/api/events/poll', 'GET', 'continue'],
          ['/api/events', 'POST', 403],
          ['/attachments/room/file', 'GET', 'continue'],
          ['/attachments/room/file', 'POST', 403],
          ['http://evil.test/api/Rooms', 'POST', 403],
          ['http://evil.test/not-logged-secret', 'GET', 403],
          ['http://127.0.0.1:7458/', 'GET', 403],
          ['/', 'POST', 403],
          ['/escape', 'GET', 403],
          ['/missing.js', 'GET', 404],
          ['/asset.js', 'GET', 200],
          ['/', 'GET', 200],
          ['/index.html', 'HEAD', 200],
        ];
        for (const [path, method, expected] of checks) {
          let result;
          const url = new URL(path, origin).href;
          await handler({
            request: () => ({ url: () => url, method: () => method }),
            async continue() { result = 'continue'; },
            async fetch(options) {
              assert.deepEqual(options, { method: 'GET' });
              return {
                ok: () => true,
                text: async () => '<head><meta name="dyna-web" content="real-engine-config"></head>',
                headers: () => ({ 'content-security-policy': "default-src 'self'", 'content-length': '10', 'content-encoding': 'gzip' }),
              };
            },
            async fulfill(response) {
              result = response.status;
              if (path === '/' && method === 'GET') {
                assert.match(response.body.toString(), /branch-shell/);
                assert.match(response.body.toString(), /real-engine-config/);
                assert.equal(response.headers['content-security-policy'], "default-src 'self'");
                assert.equal(response.headers['content-encoding'], undefined);
                assert.equal(response.headers['content-length'], undefined);
              }
              if (method === 'HEAD' && result === 200) assert.equal(response.body.length, 0);
              if (path === '/asset.js') assert.equal(response.body.toString(), 'branch-asset');
            },
          });
          assert.equal(result, expected, method + ' ' + path);
        }
        assert.ok(socketsBlocked);
        console.log('QA policy checks passed: ' + checks.length);
        closeWithPendingCookie = true;
        pendingRoute = handler({
          request: () => ({ url: () => origin + '/api/Rooms', method: () => 'POST' }),
          async continue() { assert.fail('closed context must not continue the pending request'); },
        });
        await cookieStarted.promise;
        closed();
      },
    };
  },
  async close() {
    if (!removingRoutes) cookieFinished.reject(new Error('TargetClosedError: browserContext.addCookies'));
  },
};
export const chromium = {
  async connectOverCDP(endpoint) {
    assert.equal(endpoint, 'test-endpoint');
    return {
      contexts: () => [{
        cookies: async () => [{ name: 'paired', value: 'not-logged' }],
        // The agent's tab holds the device cookie and the stored device state.
        // A QA tab copied from it must carry both, or its page is unpaired.
        storageState: async () => ({
          cookies: [{ name: 'paired', value: 'not-logged', domain: '127.0.0.1', path: '/', expires: -1, httpOnly: true, secure: false, sameSite: 'Strict' }],
          origins: [{ origin, localStorage: [{ name: 'dyna.device', value: 'paired-key' }] }],
        }),
      }],
      async newContext(options) {
        assert.equal(options.serviceWorkers, 'block');
        assert.equal(options.storageState.cookies[0].name, 'paired');
        assert.equal(options.storageState.origins[0].origin, origin, 'QA tab dropped the stored device state');
        assert.equal(options.storageState.origins[0].localStorage[0].name, 'dyna.device');
        return context;
      },
      once() {},
      async close() {},
    };
  },
};
