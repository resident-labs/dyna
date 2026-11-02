<script lang="ts">
  import { call } from "../client.ts";
  import type { WorkflowStep, WorkflowStepResponse } from "../contract.gen.ts";
  import { stepBody } from "../workflow.ts";
  import Markdown from "./Markdown.svelte";
  import RunFields from "./RunFields.svelte";

  // What one step ran and left, read when it opens: a journal row carries neither. A step that ends while open reads again.
  let { session, step, repo }: { session: string; step: WorkflowStep; repo?: string } = $props();
  let output = $state<WorkflowStepResponse>(), failed = $state(false), asked = $state(0);
  $effect(() => {
    let current = true;
    void asked; void step.ended;
    failed = false;
    call("workflow.step", { session, key: step.key }).then(read => { if (current) output = read; }, () => { if (current) failed = true; });
    return () => { current = false; };
  });
  let parts = $derived(stepBody(step, output));
</script>

<div class="detail">
  {#each parts as part, index (index)}
    <div>
      {#if part.label}<h4>{part.label}</h4>{/if}
      {#if part.kind === "fields"}<RunFields fields={part.fields} {repo} />
      {:else if part.kind === "markdown"}<Markdown rhythm="thread" text={part.text} />
      {:else if part.kind === "code"}<pre>{part.text}</pre>
      {:else}<p class:reason={part.kind === "reason"}>{part.text}</p>{/if}
    </div>
  {/each}
  {#if failed}<p class="muted" role="alert">This step did not load. <button type="button" class="retry" onclick={() => { asked++; }}>Try again</button></p>
  {:else if output && !parts.length}<p class="muted">This step left nothing else.</p>{/if}
</div>

<style>
  .detail { display: flex; flex-direction: column; gap: 8px; min-width: 0; padding: 4px 0 8px; font-size: 13px; }
  h4 { margin: 0 0 4px; font-size: 13px; font-weight: 600; line-height: 1.25; }
  p { margin: 0; overflow-wrap: anywhere; line-height: 1.45; white-space: pre-wrap; }
  .reason { color: var(--danger); }
  .muted { color: var(--muted); }
  pre { margin: 0; padding: 8px 12px; max-height: 240px; overflow: auto; border-radius: 6px; background: var(--sidebar); color: var(--text); font-family: var(--mono); font-size: 12.5px; line-height: 1.45; white-space: pre-wrap; overflow-wrap: anywhere; }
  .retry { padding: 0; border: 0; background: none; color: var(--focus); font: inherit; cursor: pointer; }
  .retry:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  @media (hover: none) { .retry { min-height: 40px; } }
</style>
