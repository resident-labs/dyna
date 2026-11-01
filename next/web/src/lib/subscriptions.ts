import type { SubscriptionInfo } from "./contract.gen.ts";

const period = (ms: number): string => {
  const minutes = Math.round(ms / 60_000);
  if (minutes < 60) return `Every ${minutes} min`;
  const hours = minutes / 60;
  return Number.isInteger(hours) ? `Every ${hours} h` : `Every ${minutes} min`;
};

// What fires a subscription, as one short phrase.
export const subscriptionWhen = ({ subscription }: SubscriptionInfo): string =>
  subscription.every ? period(subscription.every) : `On ${(subscription.on ?? []).join(", ")}`;

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
