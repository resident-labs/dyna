import type { ScriptRef } from '../api/room.ts'
import { routeHash } from '../ui/route.ts'
import { detailIDs } from '../ui/details.ts'

// A workflow script's post names its run. A post stored before the run was
// kept on it has only the sender address run:<id>, so the name is generic.
export function scriptSender(post: { from?: string; script?: ScriptRef }): ScriptRef | undefined {
  if (post.script?.runId) return post.script
  const id = post.from?.startsWith('run:') ? post.from.slice(4) : ''
  return detailIDs.run.test(id) ? { runId: id, workflow: '' } : undefined
}

// The name shown for a workflow sender: its workflow's name, or the short run
// ID when the post predates the stored name.
export function scriptLabel(ref: ScriptRef): string {
  return ref.workflow || `Workflow ${ref.runId.slice(-8)}`
}

// scriptHref is the address of the run's page.
export function scriptHref(room: string, ref: ScriptRef): string {
  return routeHash({ room, view: 'runs', session: '', turn: 0, run: ref.runId })
}
