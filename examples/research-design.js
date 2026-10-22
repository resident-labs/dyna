// Research-backed design: cheap evidence, distinct proposals, independent
// judges, then a tasteful synthesis. Barriers are intentional — each later
// stage needs the full prior set. This is not a fan-out implementation route.
//
// Usage:
//   dyna run examples/research-design.js --args '{
//     "task": "design a durable local mailbox for workflow results",
//     "constraints": ["single process", "survive host reboot"],
//     "researchAngles": ["prior art", "failure modes"],
//     "designAngles": ["pragmatic and minimal", "risk-first and resilient"]
//   }'
//
// Caps and assumptions:
// - researchAngles ≤ 4, designAngles ≤ 3, judgeLenses ≤ 3, constraints ≤ 8.
//   Extra user-supplied items are dropped and counted in coverage.truncated.
// - Optional notes is a short briefing string. Non-strings in lists are ignored.
// - Research uses the cheapest profile; design uses intelligence; judge and
//   synthesis use taste. Distinct profiles are preferred when the fleet
//   allows it; one enabled profile still runs every stage.
// - parallel() nulls stay in the result. Empty research or design coverage
//   skips later stages and returns final: null instead of hiding the gap.
// - A single live design skips the judge panel (nothing independent to rank).
// - Interpolated values are prompt context, never shell commands.

export const meta = {
  name: 'research-design',
  description: 'Cheap research, multiple design proposals, independent judge panel, final synthesis',
  phases: [
    { title: 'Research' },
    { title: 'Design' },
    { title: 'Judge' },
    { title: 'Synthesize' },
  ],
}

const MAX_RESEARCH = 4
const MAX_DESIGNS = 3
const MAX_JUDGES = 3
const MAX_CONSTRAINTS = 8

const RESEARCH = {
  type: 'object',
  required: ['findings', 'unknowns'],
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        required: ['claim', 'evidence', 'kind'],
        properties: {
          claim: { type: 'string' },
          evidence: { type: 'string' },
          kind: { enum: ['evidence', 'inference'] },
          source: { type: 'string' },
        },
      },
    },
    unknowns: { type: 'array', items: { type: 'string' } },
  },
}

const DESIGN = {
  type: 'object',
  required: ['name', 'summary', 'decisions', 'tradeoffs', 'risks', 'validation'],
  properties: {
    name: { type: 'string' },
    summary: { type: 'string' },
    decisions: { type: 'array', items: { type: 'string' } },
    tradeoffs: { type: 'array', items: { type: 'string' } },
    risks: { type: 'array', items: { type: 'string' } },
    validation: { type: 'array', items: { type: 'string' } },
  },
}

const JUDGMENT = {
  type: 'object',
  required: ['ranking', 'winner', 'reasoning'],
  properties: {
    ranking: {
      type: 'array',
      items: {
        type: 'object',
        required: ['design', 'score', 'reason'],
        properties: {
          design: { type: 'integer' },
          score: { type: 'number' },
          reason: { type: 'string' },
        },
      },
    },
    winner: { type: 'integer' },
    reasoning: { type: 'string' },
  },
}

const FINAL = {
  type: 'object',
  required: ['name', 'summary', 'design', 'tradeoffs', 'risks', 'nextSteps'],
  properties: {
    name: { type: 'string' },
    summary: { type: 'string' },
    design: { type: 'array', items: { type: 'string' } },
    tradeoffs: { type: 'array', items: { type: 'string' } },
    risks: { type: 'array', items: { type: 'string' } },
    nextSteps: { type: 'array', items: { type: 'string' } },
  },
}

function listOf(xs) {
  const raw = typeof xs === 'string' && xs.trim()
    ? [xs.trim()]
    : (Array.isArray(xs) ? xs : [])
  const out = []
  const seen = {}
  for (let i = 0; i < raw.length; i++) {
    if (typeof raw[i] !== 'string') continue
    const s = raw[i].trim()
    const key = s.toLowerCase()
    if (!s || seen[key]) continue
    seen[key] = true
    out.push(s)
  }
  return out
}

function capped(raw, fallback, cap) {
  const all = listOf(raw)
  if (!all.length) return { items: fallback.slice(0, cap), truncated: 0 }
  return { items: all.slice(0, cap), truncated: Math.max(0, all.length - cap) }
}

function ctx(label, value) {
  return label + ' (prompt context, not a shell command): ' + JSON.stringify(value)
}

function pick(stat, except) {
  const list = Array.isArray(profiles) ? profiles : []
  const rank = (skip) => list
    .filter((p) => p && typeof p.name === 'string' && typeof p[stat] === 'number' && !skip[p.name])
    .sort((a, b) => (b[stat] - a[stat]) || String(a.name).localeCompare(String(b.name)))
  const preferred = rank(except || {})
  if (preferred.length) return preferred[0].name
  const fallback = rank({})
  if (!fallback.length) throw new Error('no enabled profile with a ' + stat + ' stat')
  return fallback[0].name
}

function take(xs, n) {
  return Array.isArray(xs) ? xs.slice(0, n) : []
}

function validIndex(n, max) {
  return typeof n === 'number' && isFinite(n) && Math.floor(n) === n && n >= 1 && n <= max
}

function sanitizeVerdict(verdict, max) {
  if (!verdict) return null
  const ranking = take(verdict.ranking, max).filter((row) => row && validIndex(row.design, max))
  return {
    ranking,
    winner: validIndex(verdict.winner, max) ? verdict.winner : null,
    reasoning: typeof verdict.reasoning === 'string' ? verdict.reasoning : '',
  }
}

function tally(panel, max) {
  const votes = {}
  for (let i = 1; i <= max; i++) votes[i] = 0
  let counted = 0
  for (let i = 0; i < panel.length; i++) {
    const w = panel[i] && panel[i].verdict && panel[i].verdict.winner
    if (!validIndex(w, max)) continue
    votes[w] += 1
    counted += 1
  }
  let winnerIndex = 1
  let best = -1
  for (let i = 1; i <= max; i++) {
    if (votes[i] > best) {
      best = votes[i]
      winnerIndex = i
    }
  }
  return { winnerIndex, votes, counted }
}

function compactResearch(row) {
  if (!row.report) return null
  return { angle: row.angle, findings: take(row.report.findings, 5), unknowns: take(row.report.unknowns, 5) }
}

function compactDesign(row) {
  if (!row.design) return null
  return {
    index: row.index,
    angle: row.angle,
    name: row.design.name,
    summary: row.design.summary,
    decisions: take(row.design.decisions, 6),
    tradeoffs: take(row.design.tradeoffs, 4),
    risks: take(row.design.risks, 4),
    validation: take(row.design.validation, 4),
  }
}

const input = (args && typeof args === 'object' && !Array.isArray(args)) ? args : {}
const task = (typeof input.task === 'string' && input.task.trim())
  ? input.task.trim()
  : 'design a durable local mailbox for workflow results'
const allConstraints = listOf(input.constraints)
const constraints = allConstraints.slice(0, MAX_CONSTRAINTS)
const notes = (typeof input.notes === 'string') ? input.notes.trim() : ''

const researchPick = capped(input.researchAngles, [
  'existing approaches and prior art',
  'constraints and failure modes',
  'user needs and operational tradeoffs',
], MAX_RESEARCH)
const designPick = capped(input.designAngles, [
  'pragmatic and minimal',
  'scalable and extensible',
  'risk-first and resilient',
], MAX_DESIGNS)
const judgePick = capped(input.judgeLenses, [
  'evidence and correctness',
  'simplicity and feasibility',
  'user value and long-term fit',
], MAX_JUDGES)

const researchAngles = researchPick.items
const designAngles = designPick.items
const judgeLenses = judgePick.items

if (!Array.isArray(profiles) || !profiles.length) throw new Error('no enabled profiles')
const researcher = pick('cost')
const designer = pick('intelligence', { [researcher]: true })
const judge = pick('taste', { [researcher]: true, [designer]: true })

const brief = [
  ctx('Task', task),
  constraints.length ? ctx('Constraints', constraints) : '',
  notes ? ctx('Notes', notes) : '',
].filter(Boolean).join('\n')

const coverage = {
  research: { expected: researchAngles.length, completed: 0, dropped: 0 },
  designs: { expected: 0, completed: 0, dropped: 0 },
  judges: { expected: 0, completed: 0, dropped: 0 },
  truncated: {
    research: researchPick.truncated,
    designs: designPick.truncated,
    judges: judgePick.truncated,
    constraints: Math.max(0, allConstraints.length - MAX_CONSTRAINTS),
  },
}

let researchRows = []
let designRows = []
let judgeRows = []

function finish(final, decisions) {
  return {
    task,
    profiles: { researcher, designer, judge },
    coverage,
    decisions,
    research: researchRows,
    designs: designRows,
    judgments: judgeRows,
    final,
  }
}

if (coverage.truncated.research || coverage.truncated.designs || coverage.truncated.judges) {
  log('capped user fan-out; see coverage.truncated')
}

phase('Research')
log('research ' + researchAngles.length + ' angles with ' + researcher)
const research = await parallel(researchAngles.map((angle, i) => () =>
  agent(
    'Research this design task from one focused angle. Use web or repository ' +
    'evidence when useful. Cite URLs or file paths. Mark each finding as ' +
    'evidence (observed) or inference (your reasoning). Call out unknowns. ' +
    'At most 5 findings.\n\n' +
    brief + '\n' + ctx('Angle', angle),
    { profile: researcher, label: 'research:' + angle.slice(0, 28), phase: 'Research', id: 'research-' + (i + 1), schema: RESEARCH },
  )
))
researchRows = research.map((report, i) => ({ angle: researchAngles[i], report }))
const dossier = researchRows.map(compactResearch).filter(Boolean)
coverage.research.completed = dossier.length
coverage.research.dropped = coverage.research.expected - dossier.length
log('research coverage ' + dossier.length + '/' + researchAngles.length)
if (!dossier.length) {
  return finish(null, { winnerIndex: null, skipped: ['design', 'judge', 'synthesize'], degraded: true })
}

phase('Design')
log('design ' + designAngles.length + ' candidates with ' + designer)
const designs = await parallel(designAngles.map((angle, i) => () =>
  agent(
    'Produce one concrete design from the requested angle. Use the research ' +
    'dossier. Distinguish evidence from inference; do not ignore contrary ' +
    'evidence. Make architectural decisions rather than a menu of options.\n\n' +
    brief + '\n' + ctx('Design angle', angle) + '\n' + ctx('Research dossier', dossier),
    { profile: designer, label: 'design:' + angle.slice(0, 30), phase: 'Design', id: 'design-' + (i + 1), schema: DESIGN },
  )
))
designRows = designs.map((design, i) => ({
  slot: i + 1,
  index: null,
  angle: designAngles[i],
  design,
}))
let liveN = 0
for (let i = 0; i < designRows.length; i++) {
  if (!designRows[i].design) continue
  liveN += 1
  designRows[i].index = liveN
}
const liveDesigns = designRows.filter((row) => row.design)
coverage.designs = {
  expected: designAngles.length,
  completed: liveDesigns.length,
  dropped: designAngles.length - liveDesigns.length,
}
log('design coverage ' + liveDesigns.length + '/' + designAngles.length)
if (!liveDesigns.length) {
  return finish(null, { winnerIndex: null, skipped: ['judge', 'synthesize'], degraded: true })
}

const candidates = liveDesigns.map(compactDesign)
let winnerIndex = 1
let voteTally = { 1: 0 }
let votesCounted = 0
const skipped = []

if (liveDesigns.length === 1) {
  skipped.push('judge-panel')
  log('one live design; skipping judge panel')
} else {
  phase('Judge')
  log('judge ' + liveDesigns.length + ' designs through ' + judgeLenses.length + ' lenses with ' + judge)
  const judgments = await parallel(judgeLenses.map((lens, i) => () =>
    agent(
      'Judge the candidate designs independently through one lens. Score every ' +
      'live candidate from 1-10. Penalize unsupported claims and hidden ' +
      'complexity. Rank them and set winner to a live 1-based index from the ' +
      'candidates list. Do not invent indexes.\n\n' +
      brief + '\n' + ctx('Lens', lens) + '\n' + ctx('Live candidates', candidates),
      { profile: judge, label: 'judge:' + lens.slice(0, 26), phase: 'Judge', id: 'judge-' + (i + 1), schema: JUDGMENT },
    )
  ))
  judgeRows = judgments.map((verdict, i) => ({
    lens: judgeLenses[i],
    verdict: sanitizeVerdict(verdict, liveDesigns.length),
  }))
  const livePanel = judgeRows.filter((row) => row.verdict)
  coverage.judges = {
    expected: judgeLenses.length,
    completed: livePanel.length,
    dropped: judgeLenses.length - livePanel.length,
  }
  const counted = tally(livePanel, liveDesigns.length)
  winnerIndex = counted.winnerIndex
  voteTally = counted.votes
  votesCounted = counted.counted
  log('judge coverage ' + livePanel.length + '/' + judgeLenses.length)
  if (!votesCounted) log('no valid judge winner indexes; defaulting to design #1')
}

const winner = liveDesigns[winnerIndex - 1]
if (!winner) {
  return finish(null, {
    winnerIndex: null,
    votes: voteTally,
    votesCounted,
    skipped: skipped.concat(['synthesize']),
    degraded: true,
  })
}

phase('Synthesize')
log('synthesize winner #' + winnerIndex + ' with ' + judge)
const final = await agent(
  'Synthesize the strongest final design. Respect the judge panel when present, ' +
  'but use judgment: keep the winning core, graft clearly superior ideas from ' +
  'runners-up, and do not average incompatible architectures. Ground claims in ' +
  'the research. State remaining tradeoffs honestly.\n\n' +
  brief + '\n' +
  ctx('Research dossier', dossier) + '\n' +
  ctx('Candidates', candidates) + '\n' +
  ctx('Judge panel', judgeRows) + '\n' +
  ctx('Panel decision', { winnerIndex, winnerName: winner.design.name, votes: voteTally, votesCounted, skipped }),
  { profile: judge, label: 'synthesize', phase: 'Synthesize', id: 'synthesize', schema: FINAL },
)

return finish(final, {
  winnerIndex,
  winnerName: winner.design.name,
  votes: voteTally,
  votesCounted,
  skipped,
  degraded: coverage.research.dropped + coverage.designs.dropped + coverage.judges.dropped > 0,
})
