<script lang="ts">
  import { parseTestAllow, type BudgetForm } from './pr.ts'
  let { form = $bindable(), busy = false }: { form: BudgetForm; busy?: boolean } = $props()
</script>

<label for="rp-ci-mode">Time budget</label>
<select id="rp-ci-mode" bind:value={form.ciTimeBudget} disabled={busy} aria-describedby="rp-ci-mode-help">
  <option value="warn">Warn</option>
  <option value="enforce">Enforce</option>
</select>
<p id="rp-ci-mode-help" class="hint">Warn posts quiet notes about slow tests and jobs. Enforce can cancel runs and fail CI for time.</p>
<label for="rp-ci-warn">Run warning</label>
<input id="rp-ci-warn" type="text" bind:value={form.ciWarnAfter} disabled={busy} placeholder="2m" aria-describedby="rp-budget-help" />
<label for="rp-ci-fail">Run failure</label>
<input id="rp-ci-fail" type="text" bind:value={form.ciFailAfter} disabled={busy} placeholder="3m" aria-describedby="rp-budget-help" />
<label for="rp-ci-max">Per-test maximum</label>
<input id="rp-ci-max" type="text" bind:value={form.ciTestMax} disabled={busy} placeholder="10s" aria-describedby="rp-budget-help" />
<p id="rp-budget-help" class="hint">Use durations such as 2m or 10s. The run warning must be positive and less than the run failure limit.</p>
<label for="rp-ci-exempt">Exempt workflows</label>
<textarea id="rp-ci-exempt" rows="2" bind:value={form.ciBudgetExempt} disabled={busy} aria-describedby="rp-exempt-help"></textarea>
<p id="rp-exempt-help" class="hint">Enter one exact GitHub workflow name per line. These workflows skip the run time budget. The per-test limit still applies. Leave blank to exempt no workflows.</p>
<label for="rp-ci-allow">Slow-test allowlist</label>
<textarea id="rp-ci-allow" rows="3" bind:value={form.ciTestAllow} disabled={busy} aria-describedby="rp-allow-help"></textarea>
<p id="rp-allow-help" class="hint">Enter one exact test name per line. Dyna does not match patterns.</p>
{#each parseTestAllow(form.ciTestAllow) as id, index}
  <div class="allow-note">
    <label for="rp-ci-note-{index}"><code>{id}</code>: measured time and reason</label>
    <input id="rp-ci-note-{index}" type="text" bind:value={form.ciTestAllowNotes[id]} disabled={busy} placeholder="12 seconds: external service setup" />
    {#if !form.ciTestAllowNotes[id]?.trim()}<p class="hint">This allowance has no measured time or reason. Add a note for each new allowance.</p>{/if}
    {#if !id.includes('/')}<p class="hint warning">This name has no package path. It allows this test name in every package. Use package/name to limit it.</p>{/if}
  </div>
{/each}
<label for="rp-ci-junit">No-JUnit warning</label>
<input id="rp-ci-junit" type="checkbox" bind:checked={form.ciNoJUnitWarning} disabled={busy} aria-describedby="rp-junit-help" />
<p id="rp-junit-help" class="hint">Warn when a run has no JUnit test report. This warning is on by default.</p>

<style>
  label { font-size: 12px; font-weight: 600; color: var(--muted); }
  input, textarea, select { background: var(--rail); border: 1px solid var(--line); border-radius: 6px; color: var(--ink); font: inherit; font-size: 13.5px; padding: 4px 8px; min-width: 0; }
  input:focus, textarea:focus, select:focus { border-color: var(--iris-edge); outline: none; }
  input[type="checkbox"] { justify-self: start; accent-color: var(--iris); }
  textarea { resize: vertical; }
  .allow-note { grid-column: 1 / -1; display: flex; flex-direction: column; gap: 4px; overflow-wrap: anywhere; }
  .warning { color: var(--gold); }
  .hint { grid-column: 1 / -1; margin: 0 0 6px; color: var(--faint); font-size: 12px; }
  @media (max-width: 760px) {
    input, textarea, select { font-size: 16px; min-height: 44px; }
  }
</style>
