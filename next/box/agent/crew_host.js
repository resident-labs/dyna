"use strict";
// The script host of a crew. It runs a workflow script in Node, with no modules, files or network, and turns the script API
// into calls to the box agent, one JSON line each: fd 3 goes to the agent and fd 4 comes back. The agent owns the journal and
// the workers. Each script gets its own vm context. That context is not a sandbox: it keeps a script's values, and the
// promises of its API, in one realm, and leaves the clock and randomness out of what a resumed script can see.
const fs = require("node:fs");
const readline = require("node:readline");
const vm = require("node:vm");

const out = fs.createWriteStream(null, { fd: 3 });
const waiting = new Map();
let calls = 0;

const bridge = {
  send: (text) => out.write(text + "\n"),
  call(text, done) {
    calls++;
    waiting.set(calls, done);
    out.write(JSON.stringify({ ...JSON.parse(text), id: calls }) + "\n");
  },
  child: (script, args, parent) => runScript(script, args, parent, 1),
};

// install runs inside the script's context, from its source text, so everything it builds belongs to that context.
function install(bridge, argsText, parent, depth) {
  const forbidden = (name) => () => {
    throw new Error(name + " would differ when the run resumes. Pass the value in args.");
  };
  const RealDate = Date;
  globalThis.Date = class Date extends RealDate {
    constructor(...values) {
      if (values.length === 0) forbidden("new Date()")();
      super(...values);
    }
    static now = forbidden("Date.now()");
  };
  Math.random = forbidden("Math.random()");

  let current = "";
  const call = (message) =>
    new Promise((resolve, reject) => {
      bridge.call(JSON.stringify({ parent, phase: current, ...message }), (line) => {
        const reply = JSON.parse(line);
        if (reply.error === undefined) return resolve(reply.value);
        reject(Object.assign(new Error(reply.error), { failed: true, step: reply.step, reason: reply.error }));
      });
    });
  const reasonOf = (error) => error?.reason ?? (String(error?.message ?? error) || "the script threw");
  // An error that no step recorded still counts at the run's end.
  const failure = (error, where) => {
    const reason = reasonOf(error);
    if (!error?.failed) bridge.send(JSON.stringify({ op: "failed", parent, phase: current, payload: where + ": " + reason }));
    return { failed: true, step: error?.step ?? where, reason };
  };

  globalThis.args = JSON.parse(argsText);
  globalThis.phase = (title) => {
    current = String(title);
  };
  globalThis.log = (...values) =>
    bridge.send(JSON.stringify({ op: "log", parent, phase: current, payload: values.join(" ") }));
  globalThis.console = { log, info: log, warn: log, error: log };
  globalThis.agent = (prompt, opts) => call({ op: "agent", payload: String(prompt), opts: opts ?? {} });
  globalThis.run = (command, opts) => call({ op: "run", payload: command, opts: opts ?? {} });
  globalThis.merge = (results, opts) => call({ op: "merge", payload: results, opts: opts ?? {} });
  globalThis.post = (to, text, data) => call({ op: "post", payload: { to, text: String(text), data } });
  globalThis.parallel = (thunks) =>
    Promise.all(
      thunks.map(async (thunk, i) => {
        try {
          return await thunk();
        } catch (error) {
          return failure(error, `parallel[${i}]`);
        }
      }),
    );
  globalThis.pipeline = (items, ...stages) =>
    Promise.all(
      items.map(async (item, i) => {
        let value = item;
        for (let s = 0; s < stages.length; s++) {
          try {
            value = await stages[s](value, item, i);
          } catch (error) {
            return failure(error, `pipeline[${i}][${s}]`);
          }
        }
        return value;
      }),
    );
  globalThis.workflow = async (ref, value) => {
    if (depth > 0) throw new Error("workflow() nests one level: a child script cannot call it");
    const input = JSON.stringify(value ?? null);
    const child = await call({ op: "workflow", payload: { ref: String(ref), args: JSON.parse(input) } });
    const end = (fields) => call({ op: "workflow_end", key: child.key, payload: String(ref), ...fields });
    try {
      const result = await bridge.child(child.script, input, child.key);
      await end({ value: JSON.parse(result) });
      return JSON.parse(result);
    } catch (error) {
      const reason = reasonOf(error);
      await end({ reason });
      throw Object.assign(new Error(reason), { failed: true, step: error?.step ?? String(ref), reason });
    }
  };
}

const meta = /^((?:\s|\/\/[^\n]*\n|\/\*[\s\S]*?\*\/)*)export\s+const\s+meta\b/;

async function runScript(source, argsText, parent, depth) {
  const context = vm.createContext({});
  vm.runInContext("(" + install + ")", context)(bridge, argsText, parent, depth);
  const body = source.replace(meta, "$1const meta");
  const script = new vm.Script("(async () => {" + body + "\n})()", { filename: "workflow.js" });
  const value = await script.runInContext(context);
  const text = vm.runInContext("(value) => JSON.stringify(value === undefined ? null : value)", context)(value);
  if (text === undefined) throw new Error("the script returned a value that JSON cannot hold");
  return text;
}

function finish(fields) {
  out.write(JSON.stringify({ op: "end", ...fields }) + "\n", () => process.exit(0));
}

// A step the script started and forgot records its own failure. It must not end the run.
process.on("unhandledRejection", () => {});

const lines = readline.createInterface({ input: fs.createReadStream(null, { fd: 4 }) });
lines.on("close", () => process.exit(1));
lines.on("line", (line) => {
  const message = JSON.parse(line);
  if (message.script !== undefined) {
    runScript(message.script, message.args, "", 0)
      .then((value) => finish({ value: JSON.parse(value) }))
      .catch((error) => finish({ reason: String(error?.step ? error.message : (error?.stack ?? error)) || "the script threw" }));
    return;
  }
  const done = waiting.get(message.id);
  waiting.delete(message.id);
  done?.(line);
});
