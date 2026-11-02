import type { SubscriptionInfo } from "./contract.gen.ts";
import { count } from "./feed.ts";

const period = (ms: number): string => {
  const minutes = Math.round(ms / 60_000);
  if (minutes < 60) return `Every ${minutes} min`;
  const hours = minutes / 60;
  return Number.isInteger(hours) ? `Every ${hours} h` : `Every ${minutes} min`;
};

// What fires a subscription, as one short phrase. A long list of events reads as its count; the row's title holds them.
export const subscriptionWhen = ({ subscription }: SubscriptionInfo): string => {
  const on = subscription.on ?? [];
  return subscription.every ? period(subscription.every) : on.length > 2 ? `On ${on.length} events` : `On ${on.join(", ")}`;
};

// What a subscription does. A Run reads as its command, and a wake as the session it wakes.
export const subscriptionAction = ({ subscription }: SubscriptionInfo, name: (session: string) => string): string =>
  subscription.run ? `Runs ${subscription.run.command?.join(" ") ?? ""}` : `Wakes ${subscription.wake?.session ? name(subscription.wake.session) : "a session"}`;

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

// The closed line of a room's subscriptions: "6 hooks", then how they stand. Hooks are the ones that run something, so
// only those can pass or fail. A wake has no result and joins the count only when no hook does. Disabled ones count for
// nothing here. A failure is never folded away, and a hook that has not run yet stops the line from claiming "all passing".
export function subscriptionSummary(list: readonly SubscriptionInfo[]): { count: string; health: string; failing: boolean } {
  const live = list.filter(info => !info.disabled), hooks = live.filter(info => info.subscription.run);
  if (!hooks.length) return { count: live.length ? count(live.length, "wake") : list.length ? `${list.length} off` : "", health: "", failing: false };
  const failed = hooks.filter(failing).length;
  const health = failed ? `${failed} failing` : hooks.every(info => info.last === "passed") ? "all passing" : "";
  return { count: count(hooks.length, "hook"), health, failing: failed > 0 };
}

// The opened list: failures first, then hooks, then wakes, and disabled ones last. A row is a name, what fires it and what
// is wrong, with nothing for a healthy hook. Two hooks from one script show the first argument that tells them apart.
export function subscriptionRows(list: readonly SubscriptionInfo[], name: (session: string) => string) {
  const rows = list.map(info => {
    const run = info.subscription.run;
    const hook = run ? hookName(run.command) : { name: subscriptionAction(info, name), args: [] };
    const state = subscriptionState(info);
    return { id: info.subscription.id, name: hook.name, args: hook.args, when: subscriptionWhen(info), state: state === "Passing" ? "" : state, failing: failing(info), disabled: !!info.disabled, wake: !run,
      title: [run ? subscriptionAction(info, name) : "", info.subscription.on?.join(", ") ?? ""].filter(Boolean).join("\n") };
  });
  const rank = (row: typeof rows[number]) => row.failing ? 0 : row.disabled ? 3 : row.wake ? 2 : 1;
  return rows.map(row => ({ ...row, detail: rows.filter(other => other.name === row.name).length > 1 ? row.args[0] ?? "" : "" }))
    .sort((a, b) => rank(a) - rank(b));
}
