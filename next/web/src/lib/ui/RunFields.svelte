<script lang="ts">
  import { branchLink, commitLink, shortCommit, type Field } from "../workflow.ts";
  import CollapsibleBody from "./CollapsibleBody.svelte";
  import RunFields from "./RunFields.svelte";

  // Fields as the run page reads them: a row per fact, with a group's rows under a hairline. The Merge section, a JSON result
  // and an opened step's data all read the same.
  let { fields, repo }: { fields: Field[]; repo?: string } = $props();
  const long = 300;
</script>

{#snippet code(text: string, link: string | undefined)}
  {#if link}<a class="code" href={link} target="_blank" rel="noopener noreferrer">{text}</a>{:else}<code>{text}</code>{/if}
{/snippet}

{#snippet value(field: Field)}
  {#if field.kind === "commit"}{@render code(shortCommit(field.commit), commitLink(repo, field.commit))}
  {:else if field.kind === "branch"}{@render code(field.branch, branchLink(repo, field.branch))}
  {:else if field.kind === "link"}<a class="link" href={field.href} target="_blank" rel="noopener noreferrer">{field.href}</a>
  {:else if field.kind === "list" && field.inline}{#each field.items as item, index (index)}{#if index}{", "}{/if}{@render value(item)}{/each}
  {:else if field.kind === "list"}<ul>{#each field.items as item, index (index)}<li>{@render value(item)}</li>{/each}</ul>
  {:else if field.kind === "text" && field.text.length > long}<CollapsibleBody><span class="text">{field.text}</span></CollapsibleBody>
  {:else if field.kind === "text"}<span class="text">{field.text}</span>{/if}
{/snippet}

<dl>
  {#each fields as field, index (index)}
    {#if field.kind === "group"}
      <dt class="head">{field.label}</dt><dd class="group"><RunFields fields={field.fields} {repo} /></dd>
    {:else if field.label}<dt>{field.label}</dt><dd>{@render value(field)}</dd>
    {:else}<dd class="whole">{@render value(field)}</dd>{/if}
  {/each}
</dl>

<style>
  dl { display: grid; grid-template-columns: fit-content(40%) minmax(0, 1fr); gap: 4px 12px; margin: 0; container: fields / inline-size; font-size: 13px; line-height: 1.45; }
  dt { min-width: 0; color: var(--muted); overflow-wrap: anywhere; }
  dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
  .head { grid-column: 1 / -1; margin-top: 4px; color: var(--text); font-weight: 600; }
  .group { grid-column: 1 / -1; padding-left: 12px; border-left: 1px solid var(--line); }
  .whole { grid-column: 1 / -1; }
  .text { white-space: pre-line; }
  ul { margin: 0; padding: 0; list-style: none; }
  code, .code { font-family: var(--mono); font-size: 12.5px; overflow-wrap: anywhere; }
  .code, .link { color: var(--focus); text-decoration: none; }
  .code:hover, .link:hover { text-decoration: underline; }
  .code:focus-visible, .link:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; border-radius: 2px; }
  /* A label and its value stack in a narrow column, so neither takes the other's width. */
  @container run (max-width: 500px) {
    dl { grid-template-columns: minmax(0, 1fr); gap: 0; }
    dd { margin-bottom: 4px; }
    .head { margin-top: 8px; }
  }
</style>
