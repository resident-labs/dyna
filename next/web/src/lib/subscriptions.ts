import type { SubscriptionInfo } from "./contract.gen.ts";

const units: Record<string, number> = { h: 3_600_000, m: 60_000, s: 1_000, ms: 1 };

// A schedule as the engine writes it, such as "1h", "1h30m" or "15m", in milliseconds.
export const durationMs = (text: string): number =>
  [...text.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)].reduce((sum, [, value, unit]) => sum + Number(value) * units[unit], 0);

const period = (every: string): string => {
  const minutes = Math.round(durationMs(every) / 60_000);
  if (minutes < 60) return `Every ${minutes} min`;
  const hours = minutes / 60;
  return Number.isInteger(hours) ? `Every ${hours} h` : `Every ${minutes} min`;
};

// What fires a subscription, as one short phrase. A long list of events reads as its count, unless `whole` asks for every
// event, as the hooks list does.
export const subscriptionWhen = ({ subscription }: SubscriptionInfo, whole = false): string => {
  const on = subscription.on ?? [];
  const where = subscription.where?.length ? ` where ${subscription.where.join(", ")}` : "";
  return subscription.every ? period(subscription.every) : on.length > 2 && !whole ? `On ${on.length} events${where}` : `On ${on.join(", ")}${where}`;
};

// What a subscription does. A Run reads as its command, and a wake as the session it wakes.
export const subscriptionAction = ({ subscription }: SubscriptionInfo, name: (session: string) => string): string =>
  subscription.run ? `Runs ${subscription.run.command?.join(" ") ?? ""}` : `Wakes ${subscription.wake ? name(subscription.wake) : "a session"}`;

// Where a subscription stands, for the owner to read at a glance. Nothing to say reads as nothing.
export function subscriptionState(info: SubscriptionInfo): string {
  if (info.disabled) return "Disabled";
  if (info.running) return "Running";
  if (info.failures) return `Failing, ${info.failures} in a row`;
  return info.last === "passed" ? "Passing" : "";
}

const programs = new Set(["bash", "sh", "dash", "zsh", "python", "python3", "node", "deno", "bun", "ruby", "perl", "env", "exec", "nice", "sudo"]);
const script = /\.(sh|bash|py|js|mjs|cjs|ts|rb|pl)$/;
const setting = /^[A-Za-z_][A-Za-z0-9_]*=/;
const words = (path: string) => {
  const name = (path.split("/").at(-1) ?? path).replace(script, "").replace(/[-_.]+/g, " ").trim();
  return name && name[0].toUpperCase() + name.slice(1);
};

// A shell line, as in bash -c "cd /box/shared && bash watch.sh", read for the script it runs: the first word that names
// one, or else its first command.
function shellLine(line: string): string[] {
  const tokens = line.split(/\s+|&&|\|\||;|\|/).map(token => token.replace(/^['"]|['"]$/g, "")).filter(Boolean);
  const at = tokens.findIndex(token => script.test(token));
  if (at >= 0) return tokens.slice(at);
  return tokens.filter(token => !setting.test(token) && !programs.has(token) && !token.startsWith("-"));
}

// A hook's readable name, from what its command runs, with the arguments that follow: bash author-watch.sh levzzz5154 3
// is "Author watch" and ["levzzz5154", "3"]. The interpreter, its flags, the path and the extension are all noise.
export function hookName(command: readonly string[] | null | undefined): { name: string; args: string[] } {
  let rest = [...command ?? []];
  while (rest.length) {
    const [first, second] = rest;
    if (/^-[a-z]*c$/.test(first) && second !== undefined) { rest = shellLine(second); break; }
    if (setting.test(first) || first.startsWith("-") || programs.has(first.split("/").at(-1) ?? first)) rest = rest.slice(1);
    else break;
  }
  return rest.length ? { name: words(rest[0]) || rest[0], args: rest.slice(1) } : { name: "Hook", args: [] };
}

const failing = (info: SubscriptionInfo) => !info.disabled && (!!info.failures || info.last === "failed");

// What the list shows: every hook, and every wake that is on. A disabled wake does nothing and has nothing to tell.
const listed = (info: SubscriptionInfo) => !!info.subscription.run || !info.disabled;

// The room's subscriptions as one line, "Hooks 11", then how they stand. The count is the rows the list shows, so it
// never disagrees with them. Hooks are the ones that run something, so only those can pass or fail, and a disabled one
// counts for nothing there. A failure is never folded away, and a hook that has not run yet stops the line from claiming
// "all passing".
export function subscriptionSummary(list: readonly SubscriptionInfo[]): { count: number; health: string; failing: boolean } {
  const shown = list.filter(listed), hooks = shown.filter(info => info.subscription.run && !info.disabled);
  const failed = hooks.filter(failing).length;
  const health = failed ? `${failed} failing` : hooks.length && hooks.every(info => info.last === "passed") ? "all passing" : "";
  return { count: shown.length, health, failing: failed > 0 };
}

// The opened list: failures first, then hooks, then wakes, and disabled hooks last. A disabled wake does nothing and has
// nothing to tell, so it stays out. A row is a name, what fires it and what is wrong, with nothing for a healthy hook. Two
// hooks from one script show the first argument that tells them apart.
export function subscriptionRows(list: readonly SubscriptionInfo[], name: (session: string) => string) {
  const rows = list.filter(listed).map(info => {
    const run = info.subscription.run;
    const hook = run ? hookName(run.command) : { name: subscriptionAction(info, name), args: [] };
    const state = subscriptionState(info);
    return { id: info.subscription.id, name: hook.name, args: hook.args, when: subscriptionWhen(info), fires: subscriptionWhen(info, true), command: run?.command?.join(" ") ?? "",
      state: state === "Passing" ? "" : state, failing: failing(info), disabled: !!info.disabled, wake: !run,
      title: [run ? subscriptionAction(info, name) : "", info.subscription.on?.join(", ") ?? ""].filter(Boolean).join("\n") };
  });
  const rank = (row: typeof rows[number]) => row.failing ? 0 : row.disabled ? 3 : row.wake ? 2 : 1;
  return rows.map(row => ({ ...row, detail: rows.filter(other => other.name === row.name).length > 1 ? row.args[0] ?? "" : "" }))
    .sort((a, b) => rank(a) - rank(b));
}
