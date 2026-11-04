(function (callHost, sendHost, argsText, parent, depth) {
  // The script API of a crew, built inside the script's own VM. The box agent owns the journal and the workers: callHost asks
  // it for a step and resolves with its reply, and sendHost hands it a line that has no reply. The VM keeps a script's values,
  // and the promises of this API, in one realm. The clock and randomness stay out of what a resumed script can see.
  const forbidden = (name) => () => {
    throw new Error(name + " would differ when the run resumes. Pass the value in args.");
  };
  // The sandbox freezes the built-in globals, so these are what the script sees in their place, not changes to them.
  class ClockDate extends Date {
    constructor(...values) {
      if (values.length === 0) forbidden("new Date()")();
      super(...values);
    }
    static now = forbidden("Date.now()");
  }
  const RandomlessMath = Object.create(Math, { random: { value: forbidden("Math.random()") } });

  let current = "";
  // The lines the script sent that the box agent has not yet taken. A step the script forgot is not here: it ends with the run.
  const pending = new Set();
  const call = async (message) => {
    const reply = await callHost({ parent, phase: current, ...message });
    if (reply.error === undefined) return reply.value;
    throw Object.assign(new Error(reply.error), { failed: true, step: reply.step, reason: reply.error });
  };
  const emit = (message) => {
    const line = sendHost({ parent, phase: current, ...message }).finally(() => pending.delete(line));
    pending.add(line);
  };
  const reasonOf = (error) => error?.reason ?? (String(error?.message ?? error) || "the script threw");
  // An error that no step recorded still counts at the run's end.
  const failure = (error, where) => {
    const reason = reasonOf(error);
    if (!error?.failed) emit({ op: "failed", payload: where + ": " + reason });
    return { failed: true, step: error?.step ?? where, reason };
  };
  // A lane names the subject a step is about. It leaves the options before they are hashed, so it changes no step's key.
  const laned = (opts) => {
    const { lane, ...rest } = opts ?? {};
    return { opts: rest, lane: lane === undefined || lane === null ? "" : String(lane) };
  };

  const log = (...values) => emit({ op: "log", payload: values.join(" ") });
  const api = {
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
      return { value: text };
    } catch (error) {
      const report = String(error?.step ? error.message : error?.stack ? error + "\n" + error.stack : error) || "the script threw";
      return { failure: { reason: reasonOf(error), report, step: error?.step ?? "" } };
    } finally {
      // The script's last lines and steps reach the journal before its end does.
      while (pending.size > 0) await Promise.allSettled([...pending]);
    }
  };
})
