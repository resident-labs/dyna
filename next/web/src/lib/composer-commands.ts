import { nameProblem } from "./name-problem.ts";
import type { ProfileSummary } from "./profiles.ts";

export type CommandContext = {
  dm: boolean;
  start: (profile: string, brief: string) => Promise<void>;
  switchProfile: (profile: string) => Promise<void>;
  send: (text: string) => void;
  clear: () => void;
};
export type Command = {
  trigger: "!" | "/"; name: string;
  complete: () => string;
  run: (text: string, context: CommandContext) => Promise<void>;
  detail: string;
};
export function profileCommands(profiles: readonly ProfileSummary[]): Command[] {
  return profiles.map(profile => ({
    trigger: "!", name: profile.name,
    detail: `Taste ${profile.taste} · Intelligence ${profile.intelligence} · Cost ${profile.cost}`,
    complete: () => `!${profile.name} `,
    async run(text, context) {
      if (!context.dm) {
        if (!text) return;
        await context.start(profile.name, text);
        context.clear();
      } else {
        await context.switchProfile(profile.name);
        if (text) context.send(text); else context.clear();
      }
    },
  }));
}
// Only registered triggers claim text. In particular, slash is reserved but remains ordinary text today.
export function parsePrefix(value: string, commands: readonly Command[]) {
  const match = /^([!/])([^\s!]*)(?:\s+([\s\S]*))?$/.exec(value);
  if (!match || (match[1] === "!" && /^!\s/.test(value)) || (match[1] === "/" && !commands.some(command => command.trigger === "/"))) return undefined;
  const [, trigger, name, rest = ""] = match;
  return { trigger, name, text: rest.trim(), command: commands.find(command => command.trigger === trigger && command.name === name) };
}
export function commandProblem(value: string, commands: readonly Command[], dm: boolean) {
  const prefix = parsePrefix(value, commands);
  if (!prefix) return "";
  if (!prefix.command) return prefix.name ? `No profile named ${prefix.name}` : "";
  return !dm && !prefix.text ? "Add what the new session should do" : "";
}
export function sessionName(brief: string, taken: Iterable<string>) {
  const names = [...taken];
  const base = brief.toLowerCase().split(/\s+/).slice(0, 5).join("-").replace(/[^a-z0-9-]/g, "").replace(/^-+|-+$/g, "").slice(0, 54) || "session";
  let name = base, suffix = 1;
  while (nameProblem(name, names)) name = `${base}-${++suffix}`;
  return name;
}
