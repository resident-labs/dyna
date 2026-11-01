import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { createServer } from "node:http";
import { spawn } from "node:child_process";
import { join } from "node:path";

const [prefix, tmp] = process.argv.slice(2);
const packageDir = join(prefix, "lib/node_modules/@earendil-works/pi-coding-agent");
const catalog = JSON.parse(readFileSync(join(packageDir, "dyna-catalog.json"), "utf8"));
if (catalog.version !== 1 || !Array.isArray(catalog.plans) || !catalog.plans.length || !Array.isArray(catalog.keys) || !catalog.keys.length) {
  throw new Error("packed catalog is empty or invalid");
}
const model = catalog.keys.find((provider) => provider.provider === "deepseek")?.models[0]?.id;
if (!model) throw new Error("catalog has no DeepSeek completions model");
const home = join(tmp, "catalog-home");
mkdirSync(home);
const server = createServer((req, res) => {
  let body = "";
  req.on("data", (chunk) => { body += chunk; });
  req.on("end", () => {
    const request = JSON.parse(body);
    if (req.method !== "POST" || req.url !== "/v1/chat/completions" || request.model !== model) {
      res.writeHead(400).end("unexpected request");
      return;
    }
    received = true;
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.end('data: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}\n\ndata: {"id":"test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n');
  });
});
let received = false;
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
writeFileSync(join(home, "models.json"), JSON.stringify({ providers: { deepseek: { baseUrl: `http://127.0.0.1:${server.address().port}/v1` } } }));
const child = spawn(join(prefix, "bin/pi"), ["--offline", "--no-session", "--provider", "deepseek", "--model", model, "--api-key", "test", "-p", "hello"], {
  env: { PATH: process.env.PATH, HOME: home, PI_CODING_AGENT_DIR: home, PI_NO_ENV_CREDENTIALS: "1" },
  stdio: ["ignore", "pipe", "pipe"],
});
let output = "";
child.stdout.on("data", (chunk) => { output += chunk; });
child.stderr.on("data", (chunk) => { output += chunk; });
const code = await new Promise((resolve, reject) => { child.on("error", reject); child.on("close", resolve); });
server.close();
if (code !== 0 || !received) throw new Error(`packed Pi override failed (exit ${code}): ${output}`);
console.log("packed catalog and Pi base URL override verified");
