<script lang="ts">
  import { hue } from "../identity.ts";
  import type { Question } from "../contract.gen.ts";
  import Avatar from "./Avatar.svelte";
  import Markdown from "./Markdown.svelte";
  import QuestionAnswer from "./QuestionAnswer.svelte";
  // A question on the Requests page or in the session's DM. While it waits it reads as a request card with the attention
  // edge, and the answer box takes the place of Approve. Answered, it is one muted line, as a decided request is.
  let { question, name, root = false, operate }: { question: Question; name: string; root?: boolean; operate: boolean } = $props();
</script>
{#if question.state === "pending"}
  <article class="card">
    <p class="top"><Avatar kind="session" {name} {root} size={24} /><strong class:root style:--hue={hue(name)}>{name}</strong><span>asks</span></p>
    <div class="text"><Markdown rhythm="thread" text={question.text} /></div>
    {#if operate}<QuestionAnswer id={question.id} {name} />{:else}<p class="why">Answer from a device that can post.</p>{/if}
  </article>
{:else}
  <p class="decided"><span class="line">{name} asked {question.text}</span>{#if question.answer}<span class="line">You answered {question.answer}</span>{/if}</p>
{/if}
<style>
  .card { display: grid; gap: 6px; max-width: 620px; margin: 8px 16px; padding: 12px 16px 12px 15px; border: 1px solid var(--line); border-left: 2px solid var(--attention-fill); border-radius: 12px; background: var(--raised); font-size: 14px; }
  p { margin: 0; }
  .top { display: flex; align-items: center; gap: 8px; min-width: 0; color: var(--muted); }
  strong { font-weight: 600; color: oklch(var(--id-name) var(--hue)); overflow-wrap: anywhere; }
  strong.root { color: var(--text); }
  .text { min-width: 0; overflow-wrap: anywhere; }
  .why { color: var(--muted); font-size: 12.5px; }
  .decided { display: grid; padding: 8px 16px; color: var(--muted); font-size: 13px; }
  .line { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @media (max-width: 640px) { .card { max-width: none; } }
</style>
