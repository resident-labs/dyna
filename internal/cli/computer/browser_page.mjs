// Shared by the trace and preset holders: picks the page they act on and ends the holder with the browser.

// The page marked globalThis.__dynaReadOnlyQA === true wins; otherwise the last page of the first context.
// A holder acts on that page, so it keeps the page's own context: the same cookies and stored device
// state, which is what keeps a traced page paired with its engine.
export async function pickPage(browser) {
  for (const p of browser.contexts().flatMap((c) => c.pages())) {
    if (await p.evaluate(() => globalThis.__dynaReadOnlyQA === true).catch(() => false)) return p;
  }
  return browser.contexts()[0].pages().at(-1);
}

// Attaches to the shared browser and exits when it goes away, which also drops any CDP overrides.
export async function attach(module, endpoint) {
  const { chromium } = await import(module);
  const browser = await chromium.connectOverCDP(endpoint);
  browser.on("disconnected", () => process.exit(0));
  setInterval(() => {}, 1 << 30); // keep the process alive until SIGTERM
  return { browser, page: await pickPage(browser) };
}
