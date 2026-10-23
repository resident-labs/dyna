<script lang="ts">
  // Root's open question, as a card under the call widget. Voice reads it
  // aloud during a call, so the owner may answer by voice, or here.
  import { api } from '../../api.ts'
  import { answers } from './attention.ts'
  import { icons } from './icons.ts'
  import type { Question } from '../api/room.ts'

  // phone: touch sizes for the full-screen call.
  let { question, roomId, live = false, phone = false }: {
    question: Question
    roomId: string
    live?: boolean
    phone?: boolean
  } = $props()
  let text = $state('')
  let busy = $state(false)
  let error = $state('')

  async function answer(choice: string, typed = '') {
    if (busy) return
    busy = true
    error = ''
    try {
      await api.answerQuestion(roomId, question.id, choice, typed)
      text = ''
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      busy = false
    }
  }
  const time = (at: string) => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
</script>

<div class="qcard" class:phone role="group" aria-label="Root asks you">
  <div class="qtop"><span class="qmark" aria-hidden="true">?</span><strong>root asks</strong><time>{time(question.askedAt)}</time></div>
  <div class="qtext">{question.text}</div>
  {#if live}
    <p class="qnote"><svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true"><path d={icons.mic} fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>Voice reads this aloud. Speak to answer, or use these.</p>
  {/if}
  <div class="answers">
    {#each answers({ options: question.options ?? undefined }) as a, i (a.label)}
      <button class="option" class:gold={i === 0} disabled={busy} onclick={() => answer(a.choice, a.text)}>{a.label}</button>
    {/each}
    <form class="reply" onsubmit={(e) => { e.preventDefault(); if (text.trim()) answer('', text.trim()) }}>
      <input class="txt" placeholder="Type an answer" aria-label="Type an answer" bind:value={text} disabled={busy} />
    </form>
  </div>
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</div>

<style>
  .qcard {
    position: relative;
    background: rgba(var(--glass-ink), var(--glass-panel));
    -webkit-backdrop-filter: blur(14px);
    backdrop-filter: blur(14px);
    border: 1px solid var(--iris-edge);
    border-radius: var(--r-sm);
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45);
    padding: var(--s3);
  }
  .qtop { display: flex; align-items: baseline; gap: var(--s2); margin-bottom: 6px; font-size: var(--t-ui); }
  .qtop strong { font-weight: 600; color: var(--gold); }
  .qtop time { font-size: var(--t-sm); color: var(--faint); }
  .qmark { color: var(--gold); font-weight: 600; }
  .qtext { font-size: var(--t-body); color: var(--ink); overflow-wrap: anywhere; white-space: pre-wrap; }
  .qnote { margin: 6px 0 0; font-size: var(--t-sm); color: var(--muted); display: flex; align-items: center; gap: 6px; }
  .answers { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 10px; align-items: center; }
  .option {
    padding: 6px 12px;
    border-radius: var(--r-xs);
    background: var(--select);
    color: var(--ink-2);
    font-size: var(--t-ui);
    transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .option:hover { background: var(--raise-hover); color: var(--ink); }
  }
  .option:active { background: var(--raise-hover); }
  .option.gold { color: var(--rail); background: var(--gold); font-weight: 600; }
  .option:disabled { opacity: 0.6; cursor: default; }
  .reply { flex: 1; min-width: 100px; display: flex; }
  .txt { min-width: 0; width: 100%; background: var(--field); border: 1px solid var(--line); border-radius: var(--r-xs); color: var(--ink); font: inherit; font-size: var(--t-ui); padding: 6px 10px; }
  .phone .qtext { font-size: var(--t-lead); }
  .phone .option { flex: 1; min-height: 52px; font-size: var(--t-body); }
  .phone .reply { flex-basis: 100%; }
  .phone .txt { min-height: 44px; font-size: var(--t-body); }
  .err { margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--coral); }
</style>
