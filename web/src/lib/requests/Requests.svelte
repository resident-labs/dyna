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
  import { alsoAsked, orderQuestions, answeredLine, requestFilter, terminalCommand } from './requests.ts'

  interface Props {
    roomId: string
    questions?: Question[]
    focusId?: string
    focusRequest?: number
    onanswer: (id: string, choice: string, text: string) => unknown
    onrun: (id: string) => unknown
    ondecline: (id: string, reason: string) => unknown
    onopenfile: (attachment: Attachment) => void
  }
  let { roomId, questions = [], focusId = '', focusRequest = 0, onanswer, onrun, ondecline, onopenfile }: Props = $props()
  let filter = $state('open')
  let focused = $state('')
  let pane: HTMLElement | undefined = $state()
  let replies = $state<Record<string, string>>({})
  let busy = $state(new Set<string>())
  let declining = $state('')
  let declineReason = $state('')
  let error = $state('')
  let shown = $derived(orderQuestions(questions.filter((q) => (q.to === 'owner' || q.id === focusId) && requestFilter(q) === filter), filter))

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
    if (q) { appliedFocus = request; filter = requestFilter(q) || 'answered'; focus(q.id) }
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
  async function run(q: Question) {
    if (busy.has(q.id)) return
    busy = new Set([...busy, q.id])
    error = ''
    try { await onrun(q.id) }
    catch (e) { error = errorText(e) }
    finally { busy = new Set([...busy].filter((id) => id !== q.id)) }
  }
  async function decline(q: Question) {
    if (busy.has(q.id)) return
    busy = new Set([...busy, q.id])
    error = ''
    try {
      await ondecline(q.id, declineReason.trim())
      declining = ''
      declineReason = ''
    } catch (e) { error = errorText(e) }
    finally { busy = new Set([...busy].filter((id) => id !== q.id)) }
  }
  function terminalOutcome(q: Question): string {
    const t = q.terminal
    if (!t) return ''
    const note = q.answer?.text?.trim()
    const exit = t.exitCode === undefined ? '' : ` · exit ${t.exitCode}`
    if (t.stage === 'done' || t.stage === 'failed' || t.stage === 'declined') return `${t.stage[0]?.toUpperCase() ?? ''}${t.stage.slice(1)}${exit}${note ? `: ${note}` : ''}`
    if (t.stage === 'denied') return `Denied${q.answererName ? ` by ${q.answererName}` : ''}${note ? `: ${note}` : ''}`
    if (t.stage === 'withdrawn') return `Withdrawn${q.withdrawn ? `: ${q.withdrawn.reason}` : ''}`
    return t.stage
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
<section class="requests" bind:this={pane} aria-label="Requests">
  <TabHeader title="Requests" blurb="Questions and terminal requests wait here. Answer when you can." filters={[{ id: 'open', label: 'Open' }, { id: 'answered', label: 'Answered/closed' }, { id: 'withdrawn', label: 'Withdrawn' }]} bind:filter />
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="list">
    {#if !shown.length}
      <EmptyState title="No {filter === 'answered' ? 'answered or closed' : filter} requests">{#if filter === 'open'}Agents ask with <code>dyna request question</code> or request a terminal with <code>dyna request terminal</code>.{:else}{filter === 'answered' ? 'Answered and closed requests are kept here.' : 'Requests agents withdraw are kept here.'}{/if}</EmptyState>
    {/if}
    {#each shown as q (q.id)}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
      <article class="question" class:focused={focused === q.id} class:compact={q.state !== 'open'} tabindex="-1" data-question={q.id} onfocusin={() => focused = q.id} onclick={(e) => { focused = q.id; const target = e.target instanceof Element ? e.target : null; if (!target?.closest('button, input, a')) e.currentTarget.focus() }}>
        {#if q.state === 'open'}
          <div class="top">
            <Avatar name={q.askerName} hue={q.askerHue} emoji={q.askerEmoji} owner={q.asker === 'owner'} size={24} ring="var(--raise)" />
            <strong class="hue-{q.askerHue}">{q.askerName}</strong>
            <span class="kind">{q.kind === 'terminal' ? 'terminal' : 'question'}</span>
            <time title={new Date(q.askedAt).toLocaleString()}>{ago(q.askedAt)}</time>
            <span class="id mono">{q.id}</span>
            {#if q.toName}<span class="to">to {q.toName}</span>{/if}
          </div>
          {#if q.kind === 'terminal' && q.terminal}
            {@const t = q.terminal}
            <div class="prose question-text">{t.reason || q.text}</div>
            {#if terminalCommand(t.argv)}<pre class="command mono">{terminalCommand(t.argv)}</pre>{/if}
            <p class="terminal-answer">Run it in a terminal, then report.{#if t.cwd} <span>Folder: <code>{t.cwd}</code></span>{/if}</p>
            <div class="terminal-actions">
              <button class="review" type="button" disabled={busy.has(q.id)} onclick={() => run(q)}>Review</button>
              <button class="decline" type="button" disabled={busy.has(q.id)} onclick={() => { declining = q.id; declineReason = '' }}>Decline…</button>
            </div>
            {#if declining === q.id}
              <form class="decline-form" onsubmit={(e) => { e.preventDefault(); decline(q) }}>
                <input aria-label="Reason for declining {q.id}" placeholder="Reason (optional)" bind:value={declineReason} disabled={busy.has(q.id)} maxlength="2000" />
                <button class="decline-confirm" type="submit" disabled={busy.has(q.id)}>Decline request</button>
                <button class="decline-cancel" type="button" disabled={busy.has(q.id)} onclick={() => { declining = ''; declineReason = '' }}>Cancel</button>
              </form>
            {/if}
          {:else}
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
          {/if}
        {:else}
          <p class="clamp">{q.kind === 'terminal' ? q.terminal?.reason || q.text : q.text}</p>
          <div class="outcome">
            <span class="kind">{q.kind === 'terminal' ? 'terminal' : 'question'}</span>
            <Avatar name={q.askerName} hue={q.askerHue} emoji={q.askerEmoji} owner={q.asker === 'owner'} size={16} ring="var(--raise)" />
            <span class="asker hue-{q.askerHue}">{q.askerName}</span><span>·</span><span class="mono">{q.id}</span><span>·</span>
            {#if q.kind === 'terminal' && q.terminal}<span class="terminal-outcome {q.terminal.stage}">{terminalOutcome(q)} · {ago(q.answer?.at ?? q.withdrawn?.at ?? q.askedAt)}</span>
            {:else if q.answer}{@const answered = q.answer}<span class="answered"><span aria-hidden="true">✓</span> {answeredLine({ answererName: q.answererName, answer: answered })} · {ago(answered.at)}</span>{#if answered.source === 'card'}<Chip outline title="A card wrote this answer. You confirmed it before it was sent.">card</Chip>{/if}
            {:else if q.withdrawn}<span class="withdrawn">Withdrawn: {q.withdrawn.reason} · {ago(q.withdrawn.at)}</span>{/if}
          </div>
        {/if}
      </article>
    {/each}
  </div>
</section>

<style>
  .requests { flex: 1; min-height: 0; overflow: auto; }
  .list { display: flex; flex-direction: column; gap: var(--s3); padding: 0 var(--s5) var(--s5); }
  .question { background: var(--raise); border-radius: 8px; padding: var(--s4); width: 100%; max-width: var(--page-reading-width); outline: none; border: 1px solid transparent; }
  .question.focused { border-color: var(--iris-edge); background: var(--iris-wash); }
  .top { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; margin-bottom: 10px; font-size: var(--t-ui); }
  .top strong { font-weight: 600; max-width: 100%; overflow-wrap: anywhere; }
  time, .id, .to { color: var(--faint); font-size: var(--t-sm); }
  .to { color: var(--muted); }
  .question-text { color: var(--ink); font-size: var(--t-lead); }
  .kind { flex: none; padding: 2px 7px; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); background: var(--main); font-size: var(--t-cap); font-weight: 600; line-height: 1.2; }
  .command { margin: var(--s3) 0; padding: 10px 12px; overflow-x: auto; white-space: pre-wrap; overflow-wrap: anywhere; border-radius: 6px; background: var(--rail); color: var(--ink); font-size: var(--t-ui); }
  .terminal-answer { margin: 0 0 var(--s3); color: var(--ink-2); font-size: var(--t-ui); }
  .terminal-answer span { display: block; margin-top: 3px; color: var(--faint); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .terminal-answer code { color: var(--muted); }
  .terminal-actions, .decline-form { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; }
  .terminal-actions button, .decline-form button { min-height: 36px; padding: 7px 13px; border-radius: 5px; font-size: var(--t-ui); font-weight: 600; }
  .review { background: var(--iris); color: var(--rail); }
  .decline, .decline-cancel { background: var(--select); color: var(--ink-2); }
  .decline-form { margin-top: var(--s3); }
  .decline-form input { flex: 1 1 180px; }
  .decline-confirm { background: var(--coral); color: var(--rail); }
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
  .outcome > :global(.avatar), .outcome > span:not(.answered, .withdrawn, .terminal-outcome) { flex: none; }
  .outcome > span.asker { flex: 0 1 auto; max-width: 35%; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .answered, .withdrawn, .terminal-outcome { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; min-width: 0; font-size: var(--t-ui); }
  .answered, .terminal-outcome.done { color: var(--sage); }
  .withdrawn, .terminal-outcome.declined, .terminal-outcome.denied, .terminal-outcome.withdrawn { color: var(--muted); }
  .terminal-outcome.failed { color: var(--coral); }
  .error { color: var(--coral); padding: 0 var(--s5); }
  @media (max-width: 760px) { .list { padding: 0 var(--s4) var(--s4); } .question { padding: 14px; } .option, .terminal-actions button, .decline-form button { min-height: 44px; } input { min-height: 44px; font-size: var(--t-title); } }
</style>
