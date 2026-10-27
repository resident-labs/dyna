<script lang="ts">
  import type { Attachment, Question } from '../api/room.ts'
  import { tick } from 'svelte'
  import { errorText } from '../errorText.ts'
  import Avatar from '../ui/Avatar.svelte'
  import Chip from '../ui/Chip.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import TabHeader from '../rooms/TabHeader.svelte'
  import QuestionFiles from './QuestionFiles.svelte'
  import { markdown, ago } from '../ui/format.ts'
  import { alsoAsked, orderQuestions, answeredLine } from './questions.ts'

  interface Props {
    roomId: string
    questions?: Question[]
    focusId?: string
    focusRequest?: number
    onanswer: (id: string, choice: string, text: string) => unknown
    onopenfile: (attachment: Attachment) => void
  }
  let { roomId, questions = [], focusId = '', focusRequest = 0, onanswer, onopenfile }: Props = $props()
  let filter = $state('open')
  let focused = $state('')
  let pane: HTMLElement | undefined = $state()
  let replies = $state<Record<string, string>>({})
  let busy = $state(new Set<string>())
  let error = $state('')
  let shown = $derived(orderQuestions(questions.filter((q) => q.to === 'owner' || q.id === focusId), filter))

  async function focus(id: string, scroll = true) {
    focused = id
    await tick()
    const el = pane?.querySelector<HTMLElement>(`[data-question="${id}"]`)
    el?.focus({ preventScroll: true })
    if (scroll) el?.scrollIntoView({ block: 'nearest' })
  }
  let appliedFocus = ''
  function applyFocusRequest() {
    const request = `${focusRequest}:${focusId}`
    if (!focusId || request === appliedFocus) return
    const q = questions.find((q) => q.id === focusId)
    if (q) { appliedFocus = request; filter = q.state; focus(q.id) }
  }
  $effect(applyFocusRequest)
  async function answer(q: Question, choice = '', text = '') {
    if (busy.has(q.id) || (!choice && !text.trim())) return
    busy = new Set([...busy, q.id])
    error = ''
    try { await onanswer(q.id, choice, text); replies = { ...replies, [q.id]: '' } }
    catch (e) { error = errorText(e) }
    finally { busy = new Set([...busy].filter((id) => id !== q.id)) }
  }
  function keydown(e: KeyboardEvent) {
    if (e.ctrlKey || e.metaKey || e.altKey || (e.target instanceof Element && e.target.closest('input, textarea, [contenteditable], [role="dialog"]'))) return
    if (filter !== 'open' || !shown.length) return
    const index = shown.findIndex((q) => q.id === focused)
    if (e.key === 'j' || e.key === 'k') {
      e.preventDefault()
      const next = shown[Math.max(0, Math.min(shown.length - 1, index + (e.key === 'j' ? 1 : -1)))]
      if (next) focus(next.id)
    } else {
      const q = shown[index < 0 ? 0 : index]
      if (!q) return
      const option = /^[1-6]$/.test(e.key) ? q.options?.[Number(e.key) - 1] : undefined
      if (option) {
        e.preventDefault(); answer(q, option)
      } else if (e.key === 'r') {
        e.preventDefault(); pane?.querySelector<HTMLInputElement>(`[data-question="${q.id}"] input`)?.focus()
      }
    }
  }
</script>

<svelte:window onkeydown={keydown} />
<section class="questions" bind:this={pane} aria-label="Questions">
  <TabHeader title="Questions" blurb="Agents keep working while they wait. Answer when you can." filters={['open', 'answered', 'withdrawn'].map((f) => ({ id: f, label: f.charAt(0).toUpperCase() + f.slice(1) }))} bind:filter />
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="list">
    {#if !shown.length}
      <EmptyState title="No {filter} questions">{#if filter === 'open'}Agents ask with <code>dyna ask</code>.{:else}{filter === 'answered' ? 'Questions you answer are kept here.' : 'Questions agents withdraw are kept here.'}{/if}</EmptyState>
    {/if}
    {#each shown as q (q.id)}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
      <article class="question" class:focused={focused === q.id} class:compact={q.state !== 'open'} tabindex="-1" data-question={q.id} onfocusin={() => focused = q.id} onclick={(e) => { focused = q.id; const target = e.target instanceof Element ? e.target : null; if (!target?.closest('button, input, a')) e.currentTarget.focus() }}>
        {#if q.state === 'open'}
          <div class="top">
            <Avatar name={q.askerName} hue={q.askerHue} emoji={q.askerEmoji} owner={q.asker === 'owner'} size={24} ring="var(--raise)" />
            <strong class="hue-{q.askerHue}">{q.askerName}</strong>
            <time title={new Date(q.askedAt).toLocaleString()}>{ago(q.askedAt)}</time>
            <span class="id mono">{q.id}</span>
            {#if q.toName}<span class="to">to {q.toName}</span>{/if}
          </div>
          <div class="prose question-text">{@html markdown(q.text)}</div>
          <p class="meanwhile"><span>While waiting</span> · {q.meanwhile}</p>
          {#if q.attachments?.length}<QuestionFiles {roomId} items={q.attachments} post={q.post} onopen={onopenfile} />{/if}
          <form class="answers" onsubmit={(e) => { e.preventDefault(); answer(q, '', replies[q.id] ?? '') }}>
            {#each q.options ?? [] as option, i}
              <button class="option" class:gold={i === 0} type="button" disabled={busy.has(q.id)} onclick={() => answer(q, option)}>{option}</button>
            {/each}
            <div class="reply-field"><input aria-label="Reply to {q.id}" placeholder="Reply…" value={replies[q.id] ?? ''} oninput={(e) => replies = { ...replies, [q.id]: e.currentTarget.value }} disabled={busy.has(q.id)} maxlength="2000" enterkeyhint="send" /><button class="send" type="submit" aria-label="Send answer to {q.id}" disabled={busy.has(q.id) || !(replies[q.id] ?? '').trim()}>Send</button></div>
          </form>
          {@const others = alsoAsked(questions, q)}
          {#if others.length}
            <div class="group"><span>Also asked by</span>{#each others as other}<Avatar name={other.askerName} hue={other.askerHue} emoji={other.askerEmoji} size={22} ring="var(--raise)" />{/each}</div>
          {/if}
        {:else}
          <p class="clamp">{q.text}</p>
          <div class="outcome">
            <Avatar name={q.askerName} hue={q.askerHue} emoji={q.askerEmoji} owner={q.asker === 'owner'} size={16} ring="var(--raise)" />
            <span class="asker hue-{q.askerHue}">{q.askerName}</span><span>·</span><span class="mono">{q.id}</span><span>·</span>
            {#if q.answer}{@const answered = q.answer}<span class="answered"><span aria-hidden="true">✓</span> {answeredLine({ answererName: q.answererName, answer: answered })} · {ago(answered.at)}</span>{#if answered.source === 'card'}<Chip outline title="A card wrote this answer. You confirmed it before it was sent.">card</Chip>{/if}
            {:else if q.withdrawn}<span class="withdrawn">Withdrawn: {q.withdrawn.reason} · {ago(q.withdrawn.at)}</span>{/if}
          </div>
        {/if}
      </article>
    {/each}
  </div>
</section>

<style>
  .questions { flex: 1; min-height: 0; overflow: auto; }
  .list { display: flex; flex-direction: column; gap: var(--s3); padding: 0 var(--s5) var(--s5); }
  .question { background: var(--raise); border-radius: 8px; padding: var(--s4); width: 100%; max-width: 760px; outline: none; border: 1px solid transparent; }
  .question.focused { border-color: var(--iris-edge); background: var(--iris-wash); }
  .top { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; margin-bottom: 10px; font-size: var(--t-ui); }
  .top strong { font-weight: 600; max-width: 100%; overflow-wrap: anywhere; }
  time, .id, .to { color: var(--faint); font-size: var(--t-sm); }
  .to { color: var(--muted); }
  .question-text { color: var(--ink); font-size: var(--t-lead); }
  .meanwhile { margin: 10px 0 var(--s3); color: var(--ink-2); font-size: var(--t-ui); overflow-wrap: anywhere; }
  .meanwhile span { font-size: var(--t-sm); color: var(--faint); }
  .answers { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 14px; }
  .option { max-width: 100%; overflow-wrap: anywhere; padding: 7px 14px; border-radius: 5px; background: var(--select); color: var(--ink-2); font-size: var(--t-ui); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .option:hover { background: var(--raise-hover); }
  }
  .option:active { background: var(--select); }
  .option.gold { color: var(--rail); background: var(--gold); font-weight: 600; }
  .reply-field { display: flex; align-items: center; gap: var(--s2); flex: 1; min-width: 140px; }
  .send { flex: none; min-height: 44px; min-width: 44px; padding: var(--s2) var(--s3); border-radius: 5px; background: var(--iris); color: var(--rail); font-size: var(--t-ui); font-weight: 600; }
  input { min-width: 0; width: 100%; background: var(--field); border: 1px solid var(--line); border-radius: 5px; color: var(--ink); font: inherit; font-size: var(--t-ui); padding: 7px 10px; }
  input::placeholder { color: var(--faint); }
  input:focus-visible { outline: 2px solid var(--iris-edge); }
  button:disabled, input:disabled { opacity: 0.6; }
  .group { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; color: var(--muted); font-size: var(--t-sm); margin-top: var(--s3); }
  .clamp { margin: 0 0 var(--s1); overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: var(--t-lead); }
  .outcome { display: flex; align-items: center; gap: 5px; color: var(--faint); font-size: var(--t-sm); min-width: 0; }
  .outcome > :global(.avatar), .outcome > span:not(.answered, .withdrawn) { flex: none; }
  .outcome > span.asker { flex: 0 1 auto; max-width: 35%; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .answered, .withdrawn { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; min-width: 0; font-size: var(--t-ui); }
  .answered { color: var(--sage); }
  .withdrawn { color: var(--muted); }
  .error { color: var(--coral); padding: 0 var(--s5); }
  @media (max-width: 760px) { .list { padding: 0 var(--s4) var(--s4); } .question { padding: 14px; } .option { min-height: 44px; } input { min-height: 44px; font-size: var(--t-title); } }
</style>
