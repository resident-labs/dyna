<script lang="ts">
  import Icon, { type IconName } from "./Icon.svelte";
  // Why the page shows no rooms, centred in the feed: an icon, what happened, and what the owner can do about it.
  // A link to something that does not exist gets one line and a way back; navigate keeps the back link inside the app.
  type Kind = "unreachable" | "no-rooms" | "no-room" | "no-session" | "no-page";
  let { kind, onretry, back, navigate }: { kind: Kind; onretry?: () => void; back?: { href: string; label: string };
    navigate?: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void } = $props();
  const states: Record<Kind, { icon: IconName; title: string; line: string; command?: string }> = {
    unreachable: { icon: "plug", title: "Dyna is not reachable.", line: "This page tries again by itself. Check that the Dyna engine is running." },
    "no-room": { icon: "room", title: "This room does not exist.", line: "The address may be old, or the room may be gone. Pick a room on the left." },
    "no-session": { icon: "chat", title: "There is nothing to show for this session.", line: "It may be closed with no messages, or the address may be old." },
    "no-page": { icon: "room", title: "This page does not exist.", line: "The address may be old or mistyped." },
    "no-rooms": { icon: "room", title: "Create your first room", line: "Run this on the computer where Dyna runs. The room shows here as soon as it exists.",
      command: "dyna room create <name> --root-profile <profile>" },
  };
  let shown = $derived(states[kind]);
  // Copy answers in place for 2 seconds, as a post's Copy does.
  let copied = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;
  function copy(command: string) {
    clearTimeout(timer);
    navigator.clipboard.writeText(command).then(() => { copied = "Copied"; }, () => { copied = "Could not copy"; })
      .finally(() => { timer = setTimeout(() => { copied = ""; }, 2000); });
  }
</script>

<section class="blocked" aria-labelledby="blocked-title">
  <span class="icon"><Icon name={shown.icon} size={20} /></span>
  <h2 id="blocked-title">{shown.title}</h2>
  <p>{shown.line}</p>
  {#if shown.command}{@const command = shown.command}
    <!-- A narrow card wraps the command between words, never inside an option such as --root-profile. -->
    <div class="command"><code>{#each command.split(" ") as word, index}{index ? " " : ""}<span>{word}</span>{/each}</code><button type="button" onclick={() => copy(command)} aria-live="polite">{copied || "Copy"}</button></div>{/if}
  {#if onretry}<button type="button" class="again" onclick={onretry}>Try again</button>{/if}
  {#if back}<a class="again" href={back.href} onclick={navigate}>{back.label}</a>{/if}
</section>

<style>
  .blocked { margin: auto; width: min(500px, calc(100% - 32px)); padding: 32px 24px 24px; display: flex; flex-direction: column; align-items: center; gap: 8px;
    text-align: center; background: var(--raised); border: 1px solid var(--line); border-radius: 12px; }
  .icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--selected); color: var(--muted); }
  h2 { margin: 0; font-size: 16px; line-height: 1.25; }
  p { margin: 0; color: var(--muted); font-size: 14px; }
  .command { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: 8px; padding: 6px 6px 6px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg); }
  code { flex: 1; min-width: 0; text-align: left; font: 12.5px var(--mono); font-variant-ligatures: none; }
  code span { white-space: nowrap; }
  button, a.again { flex: none; display: inline-flex; align-items: center; height: 28px; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--raised); color: var(--text);
    font: inherit; font-size: 13px; font-weight: 500; text-decoration: none; cursor: pointer; }
  button:hover, a.again:hover { background: var(--hover); }
  .again { margin-top: 8px; }
  button:focus-visible, a.again:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
