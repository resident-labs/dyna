import { writeFile } from 'node:fs/promises';

const page = {
  async addInitScript() {},
  once() {},
  on() {},
  async exposeFunction() {},
  async goto(url) {
    console.log('QA fake goto: ' + url);
  },
  async setViewportSize(viewport) {
    console.log('QA fake viewport: ' + viewport.width + 'x' + viewport.height);
  },
  locator(selector) {
    return {
      async click() {
        console.log('QA fake click: ' + selector);
      },
      async pressSequentially(text) {
        console.log('QA fake type: ' + selector + ' <= ' + text);
      },
      async waitFor() {
        console.log('QA fake waitFor: ' + selector);
      },
    };
  },
  async screenshot(options) {
    console.log('QA fake screenshot: ' + options.path);
    await writeFile(options.path, 'PNG');
  },
};

const context = {
  async newCDPSession(target) {
    if (target !== page) throw new Error('wrong CDP target');
    return {
      async send() {
        return { targetInfo: { targetId: 'steps-target' } };
      },
      async detach() {},
    };
  },
  async routeWebSocket() {},
  async route() {},
  async unrouteAll() {},
  async close() {},
};

export const chromium = {
  async connectOverCDP() {
    return {
      contexts: () => [{
        cookies: async () => [],
        storageState: async () => ({ cookies: [], origins: [] }),
      }],
      async newContext() {
        return {
          ...context,
          async newPage() {
            return page;
          },
        };
      },
      once() {},
      on() {},
      async close() {},
    };
  },
};
