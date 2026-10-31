<script lang="ts">
  import { SvelteMap } from "svelte/reactivity";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { watchShell, type ShellJob } from "../shell.ts";
  let { session, name, onback }: { session: string; name: string; onback: () => void } = $props();
  const jobs = new SvelteMap<string, ShellJob>();
  let visible = $state(document.visibilityState === "visible");
  // The engine refuses a session that is not running; the page becoming visible again tries once more.
  let refused = $state(false);
  // The stream is open only while the owner can see this view.
  $effect(() => { if (visible) return watchShell(session, jobs, closed => { refused = closed; }); });
  // As in v1, the view shows the newest running job until the owner picks one.
  let chosen: string | undefined = $state();
  // As v1's Active jobs above Finished history: running jobs, then finished ones, each newest first. The map
  // holds jobs in the order the view saw them start or end.
  let list = $derived.by(() => { const all = [...jobs.values()].reverse(); return [...all.filter(job => job.running), ...all.filter(job => !job.running)]; });
  let shown = $derived(jobs.get(chosen ?? "") ?? list[0]);
  // The output follows its end unless the owner has scrolled up.
  let output: HTMLPreElement | undefined = $state();
  let following = $state(true);
  $effect(() => { if (following && output && shown) output.scrollTop = output.scrollHeight; });
  const pick = (job: string) => { chosen = job; following = true; };
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible) refused = false; }} />
<div class="view">
  <header class="label"><button type="button" class="back" aria-label="Back to sessions" title="Back to sessions" onclick={onback}
    {@attach button => button.focus()}><Icon name="chevron" /></button>{name}'s background jobs</header>
  {#if refused}<p class="hint">This session is not running, so its jobs cannot be shown.</p>
  {:else if !shown}<p class="hint">No job output yet.</p>
  {:else}
    <ul>{#each list as job (job.job)}
      <li><button type="button" class="job" aria-current={job.job === shown.job} title={job.command} onclick={() => pick(job.job)}>
        <code>{job.command}</code>
        {#if job.running}<span class="count">running</span>{:else if job.exitCode}<StatusPill tone="failed" label={`exited ${job.exitCode}`} />
        {:else}<span class="count">exited 0</span>{/if}</button></li>
    {/each}</ul>
    <pre bind:this={output} aria-label={`Output of ${shown.command}`} onscroll={() => {
      if (output) following = output.scrollHeight - output.scrollTop - output.clientHeight < 40;
    }}>{shown.text || "No output yet."}</pre>
    {#if !following}<button type="button" class="retry resume" onclick={() => { following = true; }}>Resume following</button>{/if}
  {/if}
</div>

<style>
  .view { display: flex; flex-direction: column; height: 100%; }
  header { gap: 6px; padding-left: 8px; }
  .back { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; cursor: pointer; }
  .back:hover { background: var(--hover); color: var(--text); }
  .back:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .back :global(svg) { transform: rotate(90deg); }
  ul { margin: 8px 0; padding: 0; list-style: none; }
  .job { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; width: 100%; padding: 5px 16px; font-size: 12px; cursor: pointer; }
  .job:hover { background: var(--hover); }
  .job[aria-current="true"] { background: var(--selected); }
  .job:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  code { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 13px ui-monospace, monospace; }
  pre { flex: 1; min-height: 0; margin: 0 12px 12px; padding: 8px 10px; overflow-y: auto; border: 1px solid var(--line); border-radius: 8px;
    background: var(--bg); font: 12px/1.5 ui-monospace, monospace; white-space: pre-wrap; overflow-wrap: anywhere; }
  .resume { align-self: center; margin-bottom: 12px; font-size: 12px; }
</style>
