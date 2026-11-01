<script lang="ts">
  import { tick } from "svelte";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import { endCause, failed, failingLine, jobTime, jobTitle } from "../jobs.ts";
  import type { ShellJob } from "../shell.ts";
  // One job's output (dock design C): its title with Copy command, a line with its session, end cause and start, the
  // command capped at 3 lines, then the output, which scrolls in its own pane. A dock tab shows it; on a phone a
  // full-height sheet does, with Back. job is undefined until the session's watch sends it.
  let { session, job, label, onback }: {
    session: { name: string; root: boolean }; job?: ShellJob; label: string; onback?: () => void;
  } = $props();

  let bad = $derived(job !== undefined && failed(job));
  let cause = $derived(job && (job.running ? "Running" : endCause(job, session.name)));
  let time = $derived(job && jobTime(job));
  let copied = $state<"yes" | "failed" | undefined>();
  async function copy() {
    if (!job) return;
    try {
      await navigator.clipboard.writeText(job.command);
      copied = "yes";
    } catch {
      copied = "failed";
    }
  }
  // The block shows at most 3 lines of the command, so a long heredoc cannot fill the pane; "Show the whole command"
  // opens the rest. clipped says whether the 3 lines hide any of it, measured again when the block resizes.
  let whole = $state(false);
  let block: HTMLElement | undefined = $state();
  let clipped = $state(false);
  let command = $derived(job?.command);
  $effect(() => {
    const element = block;
    void command;
    whole = false;
    copied = undefined;
    if (!element) return;
    const measure = () => { clipped = element.scrollHeight > element.clientHeight + 1; };
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    measure();
    return () => observer.disconnect();
  });

  // A running job's output follows its end unless the owner scrolls up. A failed job opens at its first failing line,
  // marked; with none, at its end.
  let fail = $derived(job && bad ? failingLine(job.text) : undefined);
  let output: HTMLPreElement | undefined = $state();
  let failing: HTMLElement | undefined = $state();
  let following = $state(true);
  let openedJob = "";
  $effect(() => {
    if (!job || !output) return;
    if (job.job !== openedJob) {
      openedJob = job.job;
      following = !fail;
      void tick().then(() => failing?.scrollIntoView({ block: "start" }));
    }
    void job.text;
    if (following) output.scrollTop = output.scrollHeight;
  });
  export function focus() { (output ?? block)?.focus(); }
  // LEGACY(v1-transfer): the exitCode === -1 check here, for jobs without the end field. Remove after the transfer.
  const empty = (job: ShellJob) => job.running ? "No output yet." : job.end?.kind === "signal" || !job.end && job.exitCode === -1
    ? "No output. A signal ended the job before it printed anything." : "No output.";
</script>

<section class="job" aria-label={label}>
  <header>
    <div class="title">{#if onback}<button type="button" class="back" aria-label="Back to jobs" onclick={onback}><Icon name="backChevron" /></button>{/if}
      <h3>{label}</h3>
      {#if job}<button type="button" class="mini" onclick={() => void copy()}><Icon name={copied === "yes" ? "check" : "copy"} size={14} />{copied === "yes" ? "Copied" : copied === "failed" ? "Could not copy" : "Copy command"}</button>{/if}</div>
    {#if job}
      <p class="meta"><Avatar kind="session" name={session.name} root={session.root} size={16} /><span>{session.name}</span><span class="sep">·</span>
        <span class:bad>{cause}{time ? ` after ${time}` : ""}</span><span class="sep">·</span>
        <time datetime={job.started}>{new Date(job.started).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}</time></p>
      <code class="command" class:whole><span bind:this={block}>{job.command}</span></code>
      {#if clipped || whole}<button type="button" class="more" aria-expanded={whole} onclick={() => { whole = !whole; }}>{whole ? "Show less" : "Show the whole command"}</button>{/if}
    {/if}
  </header>
  {#if job && !job.text}<p class="empty">{empty(job)}</p>
  {:else if job}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (the keyboard scrolls the output) -->
    <pre bind:this={output} tabindex="0" aria-label={`Output of ${label}`}
      onscroll={() => { if (output) following = output.scrollHeight - output.scrollTop - output.clientHeight < 40; }}
      >{#if fail}{job.text.slice(0, fail.start)}<mark bind:this={failing}>{job.text.slice(fail.start, fail.end)}</mark>{job.text.slice(fail.end)}{:else}{job.text}{/if}</pre>
    {#if !following && job.running}<button type="button" class="more resume" onclick={() => { following = true; }}>Resume following</button>{/if}
  {/if}
</section>

<style>
  .job { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; background: var(--bg); }
  header { flex: none; display: grid; gap: 6px; padding: 12px 16px 10px; border-bottom: 1px solid var(--line); }
  /* A whole command takes at most half the pane and scrolls, so the output keeps the rest. */
  header:has(.whole) { max-height: 50%; overflow-y: auto; }
  .title { display: flex; align-items: center; gap: 8px; min-width: 0; }
  h3 { flex: 1; min-width: 0; margin: 0; overflow: hidden; font-size: 15px; font-weight: 600; line-height: 1.25; text-overflow: ellipsis; white-space: nowrap; }
  .back { all: unset; display: grid; place-items: center; width: 32px; height: 32px; margin-left: -8px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .mini { all: unset; flex: none; display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 8px; border-radius: 6px;
    box-shadow: inset 0 0 0 1px var(--firm); color: var(--muted); font-size: 12.5px; font-weight: 500; cursor: pointer; }
  .mini:hover, .back:hover { background: var(--hover); color: var(--text); }
  .meta { display: flex; align-items: center; gap: 6px; min-width: 0; margin: 0; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .sep { opacity: 0.6; }
  .bad { color: var(--danger); }
  .command { display: block; padding: 6px 10px; border-radius: 6px;
    background: var(--hover); color: var(--muted); font: 12px/1.5 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; }
  .command span { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; line-clamp: 3; overflow: hidden; }
  .command.whole span { display: block; }
  .more { all: unset; justify-self: start; color: var(--focus); font-size: 12.5px; font-weight: 500; cursor: pointer; }
  .more:hover { text-decoration: underline; }
  .mini:focus-visible, .back:focus-visible, .more:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .empty { margin: 0; padding: 14px 16px; color: var(--muted); font-size: 13px; line-height: 1.45; }
  pre { flex: 1; min-height: 0; margin: 0; padding: 10px 16px; overflow-y: auto; font: 12.5px/1.55 var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; }
  pre:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  mark { display: inline-block; width: 100%; background: color-mix(in srgb, var(--danger) 14%, transparent); color: inherit; scroll-margin-top: 8px; }
  .resume { align-self: center; margin: 6px 0; }
</style>
