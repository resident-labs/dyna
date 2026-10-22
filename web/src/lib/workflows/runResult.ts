import type { RunDetail, RunResultView } from '../api/workflow.ts'
import { errorText } from '../errorText.ts'

// The calls these helpers need from the API client.
export interface ResultApi {
  runResult(id: string, agent: number): Promise<RunResultView>
}

// What the run view holds for the run's own result: the loaded result, a
// load error, or nothing.
export type LoadedResult = RunResultView | { error: string } | null

// One open worker row of a run view.
export interface WorkerRow {
  open?: boolean
  loading?: boolean
  value?: RunResultView
  error?: string
  final?: boolean
  queued?: boolean
}

export type WorkerRows = Record<number, WorkerRow>

// Run details carry only the result's summary and size; the result itself
// loads from RunResult. createResultLoader returns load(detail): it asks once
// per run and result size, drops a late answer for an earlier run, and
// clears the result when the run changes or has none.
export function createResultLoader(api: ResultApi, set: (value: LoadedResult) => void) {
  let key = ''
  return (detail: Pick<RunDetail, 'run' | 'resultBytes'> | null | undefined): void => {
    const id = detail?.run?.id
    const bytes = detail?.resultBytes ?? 0
    const next = bytes ? `${id}:${bytes}` : ''
    if (next === key) return
    if (key) set(null)
    key = next
    if (!next || !id) return
    api.runResult(id, 0).then(
      (value) => { if (key === next) set(value) },
      (e: unknown) => { if (key === next) set({ error: errorText(e) }) },
    )
  }
}

// createWorkerResults keeps the open worker rows of one run view. A row
// fetched while its worker ran is not final: refresh, called on each run
// detail update, fetches open unfinished rows again, and once more when the
// worker ends. A finished worker's row stays cached; a failed or unfinished
// row fetches again when it reopens. sync starts a new run with no rows.
// A refresh during a load is queued and runs when the load ends.
// set receives {id: {open, loading, value, error, final, queued}}.
export function createWorkerResults(api: ResultApi, set: (rows: WorkerRows) => void) {
  let rows: WorkerRows = {}
  let run = ''
  const publish = () => set({ ...rows })
  function fetch(runId: string, agent: number, final: boolean) {
    rows[agent] = { ...rows[agent], loading: true }
    publish()
    api.runResult(runId, agent).then(
      (value) => {
        const row = rows[agent]
        if (run !== runId || !row) return
        const queued = row.queued
        rows[agent] = { ...row, loading: false, value, error: undefined, final, queued: undefined }
        // A newer status came in during this load: fetch for it now.
        if (queued !== undefined && !final) fetch(runId, agent, queued)
        else publish()
      },
      (e: unknown) => {
        const row = rows[agent]
        if (run === runId && row) { rows[agent] = { ...row, loading: false, error: errorText(e), final: false }; publish() }
      },
    )
  }
  return {
    toggle(runId: string, agent: number, status = 'ok') {
      if (runId !== run) { rows = {}; run = runId }
      const row = rows[agent]
      if (row?.open) { rows[agent] = { ...row, open: false }; publish(); return }
      rows[agent] = { ...row, open: true }
      if (!row || (!row.final && !row.loading)) fetch(runId, agent, status !== 'running')
      else publish()
    },
    refresh(runId: string, agents: readonly { id: number; status: string }[] = []) {
      if (runId !== run) return
      for (const a of agents) {
        const row = rows[a.id]
        if (!row?.open || row.final || row.error) continue
        if (row.loading) rows[a.id] = { ...row, queued: a.status !== 'running' }
        else fetch(runId, a.id, a.status !== 'running')
      }
    },
    sync(runId: string) { if (runId !== run) { rows = {}; run = runId; publish() } },
    reset() { rows = {}; run = ''; publish() },
  }
}
