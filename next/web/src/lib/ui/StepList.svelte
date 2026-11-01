<script lang="ts">
  import Icon, { type IconName } from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { count } from "../feed.ts";
  import ReadThumb from "./ReadThumb.svelte";
  import { stepTime, stepTitle, type StepKind } from "../steps.ts";
  import { readMedia, type MediaRow } from "../media.ts";
  import type { TranscriptStep } from "../contract.gen.ts";
  // A past turn's steps, as in v1's timeline: an icon for what each step did, its target and time, and its output on
  // expand. A failed step opens by itself. Long lists show the last steps first, and the earlier ones open in place.
  // A toggled step differs from its default, so `opened` keeps one set of keys however a step starts.
  let { steps, truncated, id, opened, ontoggle, session, onmedia }: {
    steps: TranscriptStep[]; truncated: boolean; id: string; opened: ReadonlySet<string>; ontoggle: (key: string) => void;
    session: string; onmedia: (row: MediaRow) => void;
  } = $props();
  const shown = 8;
  const icons: Record<StepKind, IconName> = { read: "file", edit: "edit", write: "write", search: "search", list: "list", run: "terminal", fetch: "globe", tool: "tool" };
  let all = $derived(opened.has(`${id}/all`));
  let hidden = $derived(Math.max(0, steps.length - shown));
  let hiddenFailures = $derived(steps.slice(0, hidden).filter(step => step.failed).length);
</script>

{#if hidden}<button type="button" class="earlier" aria-expanded={all} onclick={() => ontoggle(`${id}/all`)}>
  <span class="chevron" class:open={all}><Icon name="chevron" size={14} /></span>
  <span>{#if all}Show the last {shown}{:else}{hidden} earlier {hidden === 1 ? "step" : "steps"}{#if hiddenFailures}{" · "}<StatusPill tone="failed" label="{hiddenFailures} failed" word />{/if}{/if}</span>
</button>{/if}
<ol aria-label={count(steps.length, "step")}>
  {#each steps as step, i (i)}
    {#if all || i >= hidden}
      {@const key = `${id}/${i}`}
      {@const title = stepTitle(step)}
      {@const time = stepTime(step)}
      {@const open = !!step.preview && opened.has(key) !== step.failed}
      {#snippet line()}
        <Icon name={icons[title.kind]} size={14} />
        <span class="what">
          <span class="verb">{title.verb}</span>
          <code title={title.full || undefined}>{title.target}</code>
          {#if time}<span class="time">{time}</span>{/if}
          {#if step.failed}<StatusPill tone="failed" label="Failed" word />{/if}
        </span>
      {/snippet}
      {@const media = step.image && readMedia(session, step.turn, step.started, title.full, step.image)}
      <li class:open>
        <!-- A button cannot hold the thumbnail's button, so the two sit side by side. -->
        <div class="row">
          {#if step.preview}
            <button type="button" class="step" aria-expanded={open} onclick={() => ontoggle(key)}>
              {@render line()}<span class="chevron"><Icon name="chevron" size={14} /></span>
            </button>
          {:else}<div class="step">{@render line()}<span class="chevron"></span></div>{/if}
          {#if media}<ReadThumb src={media.src} name={media.attachments[0].name} onopen={() => onmedia(media)} />{/if}
        </div>
        {#if step.preview && open}<pre>{#if title.full}<span class="target">{title.kind === "run" ? "$ " : ""}{title.full}</span>{"\n"}{/if}{step.preview}</pre>{/if}
      </li>
    {/if}
  {/each}
</ol>
{#if truncated}<p class="unavailable">Some step details are not available.</p>{/if}

<style>
  .unavailable { margin: 0; padding: 4px 0; }
  ol { margin: 0; padding: 0; list-style: none; width: 100%; min-width: 0; }
  button { all: unset; box-sizing: border-box; cursor: pointer; border-radius: 6px; }
  button:hover { background: var(--hover); color: var(--text); }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  /* The wash reaches 4 px past the text, so the text keeps its edge. */
  .earlier, .step { display: flex; align-items: center; gap: 8px; min-width: 0; width: calc(100% + 8px); min-height: 28px; margin: 0 -4px; padding: 0 4px; }
  .row { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .row > .step { flex: 1; width: auto; }
  .what { flex: 1; display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .verb { flex: none; }
  code { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--mono); font-size: 12.5px; }
  .open code { color: var(--text); }
  .time { flex: none; font-size: 11px; font-variant-numeric: tabular-nums; }
  .chevron { display: inline-flex; flex: none; width: 14px; transform: rotate(-90deg); transition: transform 150ms ease-out; }
  .open .chevron, .chevron.open { transform: none; }
  pre { margin: 2px 0 8px 22px; padding: 8px 12px; max-height: 240px; overflow: auto; border-radius: 6px; background: var(--sidebar); color: var(--text);
    font-family: var(--mono); font-size: 12.5px; line-height: 1.45; white-space: pre-wrap; overflow-wrap: anywhere; }
  /* A row stays one line, so the output opens with its whole target, which a phone has no room or tooltip for. */
  .target { color: var(--muted); }
  @media (prefers-reduced-motion: reduce) { .chevron { transition: none; } }
  @media (hover: none) { .earlier, .step { min-height: 40px; } }
</style>
