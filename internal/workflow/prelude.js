// dyna workflow prelude: builds the public script API on top of the
// Go-backed hooks (__spawn, __workflow, __post, __phase, __log, __logSystem).
"use strict";

// Goja does not propagate async context, so instrumented inline pipeline
// callbacks receive an immutable lexical context from a per-invocation
// callback factory. The engine-wide slot is a synchronous best-effort fallback
// for callbacks passed by identifier.
const __planALS = { current: null };
const __planStageFactories = new WeakMap();
const __planContexts = new WeakSet();

function makePlanStageCallback(factory, ctx, stage) {
  let callback;
  const recurse = function (...args) {
    return Reflect.apply(callback, undefined, args);
  };
  callback = factory(ctx, stage, recurse);
  return callback;
}

globalThis.__planStage = function (factory, callbackName) {
  const stage = function (...args) {
    return Reflect.apply(makePlanStageCallback(factory, null, stage), undefined, args);
  };
  // A named function expression is rewritten so its lexical self binding is
  // this stable wrapper. Mirror the observable name/arity from one inert
  // implementation instance; defaults and bodies do not execute here.
  const shape = makePlanStageCallback(factory, null, stage);
  Object.defineProperty(stage, "name", { value: callbackName || shape.name, configurable: true });
  Object.defineProperty(stage, "length", { value: shape.length, configurable: true });
  __planStageFactories.set(stage, factory);
  return stage;
};

function applyPlanCtx(opts, explicitCtx) {
  opts = Object.assign({}, opts || {});
  const ctx = explicitCtx || __planALS.current;
  if (ctx) {
    if (ctx.groupId) opts.__planGroupId = String(ctx.groupId);
    if (ctx.stage != null) opts.__stageIndex = ctx.stage;
    if (ctx.item != null) opts.__itemIndex = ctx.item;
  }
  return opts;
}

globalThis.log = function (msg) { __log(String(msg)); };
// phase(title, { lane?, stage? }): lane and stage place the phase's agents on the
// run page's lanes grid (one row per lane, one column per stage).
globalThis.phase = function (title, opts) {
  const o = opts && typeof opts === "object" ? opts : {};
  __phase(String(title), "", o.lane == null ? "" : String(o.lane), o.stage == null ? "" : String(o.stage));
};

globalThis.__planAgent = function (planNodeId, ...args) {
  let ctx;
  const last = args[args.length - 1];
  if (last && __planContexts.has(last)) {
    ctx = args.pop();
  }
  const prompt = args[0];
  const opts = applyPlanCtx(args[1], ctx);
  opts.__planNodeId = String(planNodeId);
  return __spawn(String(prompt), opts);
};
globalThis.__planPhase = function (planNodeId, title, opts) {
  const o = opts && typeof opts === "object" ? opts : {};
  __phase(String(title), String(planNodeId), o.lane == null ? "" : String(o.lane), o.stage == null ? "" : String(o.stage));
};
globalThis.__planLog = function (planNodeId, msg) {
  __log(String(msg), String(planNodeId));
};
globalThis.__planWorkflow = function (planNodeId, nameOrRef, workflowArgs) {
  return workflow(nameOrRef, workflowArgs, planNodeId);
};
globalThis.__planParallel = async function (planGroupId, thunks) {
  __groupStart(String(planGroupId), "parallel");
  try {
    return await parallel(thunks);
  } finally {
    __groupEnd(String(planGroupId), "parallel");
  }
};
globalThis.__planPipeline = async function (planGroupId, items, ...stages) {
  __groupStart(String(planGroupId), "pipeline");
  try {
    return await executePipeline(items, stages, (stage, cur, item, i, s) => {
      const ctx = Object.freeze({ groupId: String(planGroupId), stage: s, item: i });
      __planContexts.add(ctx);
      const prev = __planALS.current;
      __planALS.current = ctx;
      __planContext(ctx.groupId, ctx.stage, ctx.item);
      try {
        const factory = __planStageFactories.get(stage);
        const callback = factory ? makePlanStageCallback(factory, ctx, stage) : stage;
        // Keep the public stage contract at exactly three arguments.
        return Reflect.apply(callback, undefined, [cur, item, i]);
      } finally {
        __planALS.current = prev;
        if (prev) __planContext(prev.groupId, prev.stage, prev.item);
        else __planContext(null, -1, -1);
      }
    });
  } finally {
    __groupEnd(String(planGroupId), "pipeline");
  }
};

// remaining is a live, read-only per-run budget. Unlimited profiles report
// null; completed calls remain consumed because maxCallsPerRun is a lifetime
// cap rather than a concurrency limit.
for (const profile of profiles) {
  const name = profile.name;
  Object.defineProperty(profile, "remaining", {
    enumerable: true,
    configurable: false,
    get: function () { return __profileRemaining(name); },
  });
}

// agent(prompt, opts?) -> Promise<string | object>
// opts: { profile, label, phase, lane, stage, schema, cwd, timeout,
//         isolation: { type: "worktree", retention?: "changed" | "all" | "none" } }
globalThis.agent = function (prompt, opts) {
  return __spawn(String(prompt), applyPlanCtx(opts));
};

// post(to, text, data?) or post(to, { text, data }) -> { id, deliveries, skipped?, error? }
// to is one @mention string or an array of them; the script posts as the run.
// data is an object or array that the feed shows as a key and value view.
globalThis.post = function (to, text, data) {
  return arguments.length > 2 ? __post(to, text, data) : __post(to, text);
};

// worktrees() -> engine-verified snapshots of retained worktrees in this run.
// A new object graph is returned on every call; scripts cannot mutate the
// registry or the shared engine state.
Object.defineProperty(globalThis, "worktrees", {
  enumerable: true,
  configurable: false,
  writable: false,
  value: function () { return __worktrees(); },
});

// workflow(nameOrRef, args?) -> Promise<any>
// Arguments cross an isolated JS runtime boundary, so require JSON values.
globalThis.workflow = function (nameOrRef, workflowArgs, planNodeId) {
  let encoded;
  try {
    encoded = workflowArgs === undefined ? undefined : JSON.stringify(workflowArgs);
    if (workflowArgs !== undefined && encoded === undefined) {
      throw new Error("value has no JSON representation");
    }
  } catch (e) {
    return Promise.reject(new Error("workflow args must be JSON-serializable: " + String((e && e.message) || e)));
  }
  return __workflow(String(nameOrRef), encoded, planNodeId);
};

// parallel(thunks) -> Promise<any[]>  (barrier; failures resolve to null)
globalThis.parallel = async function (thunks) {
  const settled = await Promise.allSettled(
    thunks.map((t) => Promise.resolve().then(t))
  );
  return settled.map((r) => {
    if (r.status === "fulfilled") return r.value;
    __logSystem("parallel: task failed: " + String((r.reason && r.reason.message) || r.reason));
    return null;
  });
};

// pipeline(items, ...stages) -> Promise<any[]>  (no barrier between stages;
// a stage that throws drops that item to null and skips its remaining stages)
globalThis.pipeline = function (items, ...stages) {
  return executePipeline(items, stages, (stage, cur, item, i) => stage(cur, item, i));
};

function executePipeline(items, stages, invoke) {
  return Promise.all(items.map(async (item, i) => {
    let cur = item;
    for (let s = 0; s < stages.length; s++) {
      try {
        cur = await invoke(stages[s], cur, item, i, s);
      } catch (e) {
        __logSystem("pipeline: item " + i + " dropped: " + String((e && e.message) || e));
        return null;
      }
    }
    return cur;
  }));
}

// sleep(ms): pacing helper for polling loops.
globalThis.sleep = function (ms) {
  return new Promise((res) => setTimeout(res, ms));
};
