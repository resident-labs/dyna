import type { ListChange } from '../api/event.ts'
import type { Page } from '../api/wire.ts'
import type { RunDetail, RunList } from '../api/workflow.ts'
import type { ShellJob } from '../api/terminal.ts'
import { errorText } from '../errorText.ts'
import { createReloadGate } from '../ui/reloadGate.ts'

// The app's current selection. A load answers only while the room and visit
// it started in are still current.
export interface WorkflowSelection {
  room: string
  visit: number
  id: string
  running: boolean
  visible: boolean
}

// The calls the controller makes.
export interface WorkflowApi {
  runs(room: string): Promise<RunList>
  runDetail(id: string): Promise<RunDetail>
  shellJobs(room: string, before?: string): Promise<Page<ShellJob>>
}

// What the controller hands back to the app.
export interface WorkflowHooks {
  current(): WorkflowSelection
  list(list: RunList): void
  listError(error: string): void
  detail(detail: RunDetail): void
  detailError(error: string): void
  jobs(page: Page<ShellJob>, append: boolean): void
  jobsError(error: string): void
  patchJobs(change: ListChange): void
}

// Runs and shell jobs: the room's background work. This controller is the
// one source of both lists; the sidebar, the runs view and a session's
// context all read what it loads.
export function createWorkflowData(api: WorkflowApi, hooks: WorkflowHooks) {
  let alive = true, listRequest = 0, detailRequest = 0, jobsRequest = 0, polling = false, timer: ReturnType<typeof setInterval> | undefined
  const reload = createReloadGate(() => hooks.current(), () => alive)
  const selected = (value: WorkflowSelection) => {
    const now = hooks.current()
    return alive && value.room === now.room && value.visit === now.visit
  }
  function loadList() { return reload('list', async (valid: () => boolean) => {
    const value = hooks.current(), request = ++listRequest
    if (!alive || !value.room) return
    const current = () => valid() && selected(value) && request === listRequest
    try {
      const list = await api.runs(value.room)
      if (current()) hooks.list(list)
    } catch (e) { if (current()) hooks.listError(String(e)) }
  }) }
  async function loadDetail() {
    const value = hooks.current(), request = ++detailRequest
    if (!alive || !value.id) return
    const current = () => selected(value) && value.id === hooks.current().id && request === detailRequest
    try {
      const detail = await api.runDetail(value.id)
      if (current()) hooks.detail(detail)
    } catch (e) { if (current()) hooks.detailError(String(e)) }
  }
  // App keeps the loaded history and sorts the shared job list.
  function loadJobs(before?: string) { return (before ? reload.patch : reload)('jobs', async (valid: () => boolean) => {
    const value = hooks.current(), request = ++jobsRequest
    if (!alive || !value.room) return
    const current = () => valid() && selected(value) && request === jobsRequest
    try {
      const page = await api.shellJobs(value.room, before)
      if (current()) hooks.jobs(page, !!before)
    } catch (e) { if (current()) hooks.jobsError(errorText(e)) }
  }) }
  return {
    loadList, loadDetail, loadJobs,
    patchJobs(change: ListChange) {
      return reload.patch('jobs', (current: () => boolean) => { if (current()) hooks.patchJobs(change) })
    },
    start() {
      if (!alive) return
      clearInterval(timer)
      timer = setInterval(() => {
        const value = hooks.current()
        if (polling || !value.visible || !value.running) return
        polling = true
        loadDetail().finally(() => (polling = false))
      }, 2000)
    },
    destroy() { alive = false; clearInterval(timer) },
  }
}
