(function (callHost, sendHost, argsText, parent, depth, serviceText) {
  // The script API of a crew, built inside the script's own VM. The box agent owns the journal and the workers: callHost asks
  // it for a step and resolves with its reply, and sendHost hands it a line that has no reply. The VM keeps a script's values,
  // and the promises of this API, in one realm. The clock and randomness stay out of what a resumed script can see.
  const forbidden = (name) => () => {
    throw new Error(name + " would differ when the run resumes. Pass the value in args.");
  };
  // The sandbox freezes the built-in globals, so these are what the script sees in their place, not changes to them.
  const RealDate = Date;
  const ClockDate = class Date extends RealDate {
    constructor(...values) {
      if (values.length === 0) forbidden("new Date()")();
      super(...values);
    }
    static now = forbidden("Date.now()");
  };
  const RandomlessMath = Object.create(Math, { random: { value: forbidden("Math.random()") } });

  // A run for a subscription serves its events. Its body runs once for each box start, and a VM of its own runs the same script
  // again for each event: that VM's body replays the stored steps of the box start, and serve() hands it the event. Steps have
  // a scope: the box start for the body, and the queue row for a handler, so a redelivered row finds its own steps again.
  // service is null for a run that serves nothing. handler is set in the VM of an event.
  const service = JSON.parse(serviceText);
  let scope = service?.scope ?? "";
  let row = service?.row ?? 0;
  let replaying = service?.handler != null;
  let served = false;
  let registered = null;
  let handled = null;

  let current = "";
  const call = async (message) => {
    const reply = await callHost({ parent, phase: current, scope, event: row, ...message });
    if (reply.error === undefined) return reply.value;
    throw Object.assign(new Error(reply.error), { failed: true, step: reply.step, reason: reply.error, rawText: reply.rawText });
  };
  // Lines reach the box agent one after another in the order the script wrote them, as the sandbox runs calls side by side.
  let lines = Promise.resolve();
  const emit = (message) => {
    if (replaying) return; // the body's lines were written when the box started
    const line = { parent, phase: current, scope, event: row, ...message };
    lines = lines.then(() => sendHost(line));
  };
  const reasonOf = (error) => error?.reason ?? (String(error?.message ?? error) || "the script threw");
  // An error that no step recorded still counts at the run's end.
  const failure = (error, where) => {
    const reason = reasonOf(error);
    if (!error?.failed) emit({ op: "failed", payload: where + ": " + reason });
    return { failed: true, step: error?.step ?? where, reason, rawText: error?.rawText };
  };
  // In a handler a step's lane is its key unless the script names another.
  const handlerKey = () => (!replaying && service?.handler ? service.handler.key : "");
  // A lane names the subject a step is about. It leaves the options before they are hashed, so it changes no step's key.
  const laned = (opts) => {
    const { lane, ...rest } = opts ?? {};
    return { opts: rest, lane: lane === undefined || lane === null ? handlerKey() : String(lane) };
  };

  // Strings stay as they are and arrays and objects are JSON, as console.log shows them. A BigInt inside one becomes a string and
  // a cycle, which JSON cannot hold, falls back to String().
  const log = (...values) => {
    const line = values.map((value) => {
      if (typeof value !== "object" || value === null || value instanceof Error) return String(value);
      try {
        return JSON.stringify(value, (_, inner) => (typeof inner === "bigint" ? String(inner) : inner));
      } catch {
        return String(value);
      }
    });
    emit({ op: "log", payload: line.join(" ") });
  };
  const api = {
    state: { get: () => call({op: "state_get"}), set: (value) => call({op: "state_set", payload: value}) },
    args: JSON.parse(argsText),
    phase: (title) => {
      current = String(title);
    },
    log,
    console: { log, info: log, warn: log, error: log },
    // status() is the run's one line of what is happening. It is not a step, so a resume does not replay it.
    status: (text) => emit({ op: "status", payload: String(text) }),
    agent: (prompt, opts) => call({ op: "agent", payload: String(prompt), ...laned(opts) }),
    run: (command, opts) => call({ op: "run", payload: command, ...laned(opts) }),
    merge: (results, opts) => call({ op: "merge", payload: results, opts: opts ?? {} }),
    post: (to, text, data, opts) => {
      const { opts: rest, lane } = laned(opts);
      if (Object.keys(rest).length) return Promise.reject(new Error("post takes only {lane} as options"));
      return call({ op: "post", payload: { to, text: String(text), data }, lane });
    },
    parallel: (thunks) =>
      Promise.all(
        thunks.map(async (thunk, i) => {
          try {
            return await thunk();
          } catch (error) {
            return failure(error, `parallel[${i}]`);
          }
        }),
      ),
    pipeline: (items, ...stages) =>
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
      ),
    // serve(handler) makes the run a service: the script's body is its setup, and each event of the subscription calls
    // handler(event, key, state) once its body has finished.
    serve: (handler) => {
      if (depth > 0) throw new Error("serve() belongs in the run's own script, not in one that workflow() runs");
      if (!service) throw new Error("serve() needs a subscription: dyna subscription add --workflow starts a service, and workflow run does not");
      if (served) throw new Error("serve() was already called; call it once, after setup");
      served = true;
      registered = handler;
    },
    workflow: async (ref, value) => {
      if (depth > 0) throw new Error("workflow() nests one level: a child script cannot call it");
      return call({ op: "workflow", payload: { ref: String(ref), args: JSON.parse(JSON.stringify(value ?? null)) } });
    },
    Date: ClockDate,
    Math: RandomlessMath,
  };

  // run starts the script with the API as its names and answers how it ended: with its result as JSON text, or with a
  // failure. Reason is how a parent script reads it, and report is what a failed run shows.
  return async (script) => {
    try {
      const text = JSON.stringify((await script(api)) ?? null);
      if (text === undefined) throw new Error("the script returned a value that JSON cannot hold");
      if (service && depth === 0 && !served) {
        throw new Error("the script's body ended without calling serve(). A workflow subscription runs as a service: call serve(async (event, key, state) => { … }) in the script to handle each event (events.md in the dyna-workflows skill)");
      }
      if (service?.handler && depth === 0) {
        // The body has ended, so its setup has replayed under the box start's scope. The event's steps take the row's.
        scope = "event:" + service.handler.row;
        row = service.handler.row;
        replaying = false;
        try {
          await registered(service.handler.event, service.handler.key, api.state);
          handled = { value: "null" };
        } catch (error) {
          handled = failure(error, "the handler");
        }
      }
      if (handled) {
        const outcome = handled;
        return outcome.failed ? { failure: { reason: outcome.reason, report: outcome.reason, step: outcome.step, rawText: outcome.rawText } } : outcome;
      }
      return { value: text, served: served && !service.handler };
    } catch (error) {
      const report = String(error?.step ? error.message : error?.stack ? error + "\n" + error.stack : error) || "the script threw";
      return { failure: { reason: reasonOf(error), report, step: error?.step ?? "", rawText: error?.rawText } };
    } finally {
      // The script's last lines reach the journal before its end does.
      await lines;
    }
  };
})
