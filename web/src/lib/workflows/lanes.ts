// Lanes by stages: a run whose phases share stages ("CH prepare", "BS prepare",
// "CH review", ...) reads as a grid. A row is a lane, a column is a stage, and a
// cell holds every phase group of that lane and stage. A run whose phases do
// not share stages gets no grid, and the page keeps its plain phase list.

export type CellState = 'running' | 'error' | 'ok'

// PhaseInput is one phase group of a run, in run order. lane and stage are set
// when the script names them; otherwise they come from the phase name.
export interface PhaseInput {
  phase: string
  state: CellState
  done: number
  total: number
  lane?: string
  stage?: string
}

export interface LaneCell {
  state: CellState
  done: number
  total: number
  // runs counts the phase groups in the cell: review, fix, review is 2 reviews.
  runs: number
  // phases holds the indexes of those groups in the input.
  phases: number[]
}

export interface Lane {
  name: string
  // cells has one entry per stage; null where the lane never reached it.
  cells: (LaneCell | null)[]
  state: CellState
}

export interface LaneGrid {
  stages: string[]
  lanes: Lane[]
}

// An unnamed stage is the column for phases that do not split into lane + stage.
export const otherStage = ''
const minSplitShare = 0.7

// splitPhase reads "<lane> <stage>" at the last space.
export function splitPhase(phase: string): { lane: string; stage: string } | null {
  const name = phase.trim()
  const at = name.lastIndexOf(' ')
  if (at <= 0) return null
  return { lane: name.slice(0, at).trim(), stage: name.slice(at + 1) }
}

function where(p: PhaseInput): { lane: string; stage: string } | null {
  if (p.lane && p.stage) return { lane: p.lane, stage: p.stage }
  return splitPhase(p.phase)
}

const worst = (a: CellState, b: CellState): CellState => (a === 'error' || b === 'error' ? 'error' : a === 'running' || b === 'running' ? 'running' : 'ok')

// laneGrid places each phase in a lane and a stage, or returns null when the
// phases have no shared stages. Explicit lane and stage always count; names
// count only when most phases split and a stage repeats across two lanes.
export function laneGrid(phases: PhaseInput[]): LaneGrid | null {
  const spots = phases.map(where)
  const explicit = phases.some((p) => p.lane && p.stage)
  const split = spots.filter((s) => s !== null).length
  if (!phases.length || (!explicit && split < phases.length * minSplitShare)) return null
  const lanesOf = new Map<string, Set<string>>()
  for (const s of spots) if (s) lanesOf.set(s.stage, (lanesOf.get(s.stage) ?? new Set()).add(s.lane))
  if (![...lanesOf.values()].some((l) => l.size > 1)) return null

  const stages = [...lanesOf.keys()]
  if (spots.some((s) => !s)) stages.push(otherStage)
  const lanes = new Map<string, Lane>()
  phases.forEach((p, i) => {
    const spot = spots[i] ?? { lane: p.phase, stage: otherStage }
    let lane = lanes.get(spot.lane)
    if (!lane) lanes.set(spot.lane, (lane = { name: spot.lane, cells: stages.map(() => null), state: 'ok' }))
    const col = stages.indexOf(spot.stage)
    const cell = lane.cells[col]
    if (cell) {
      cell.state = worst(cell.state, p.state)
      cell.done += p.done
      cell.total += p.total
      cell.runs++
      cell.phases.push(i)
    } else {
      lane.cells[col] = { state: p.state, done: p.done, total: p.total, runs: 1, phases: [i] }
    }
    lane.state = worst(lane.state, p.state)
  })
  return { stages, lanes: [...lanes.values()] }
}

const rank: Record<CellState, number> = { error: 0, running: 1, ok: 2 }

// byUrgency lists failed lanes first, then running ones, then finished ones.
// The sort is stable, so lanes of one state keep their first-seen order.
export const byUrgency = (lanes: Lane[]): Lane[] => [...lanes].sort((a, b) => rank[a.state] - rank[b.state])
