// Isolated fan-out for a large repository change.
// Cheap parallel recon, one planner, up to two budget-sized waves of
// disjoint worktree implementers with streamed adversarial review (and at
// most one repair each), then a single integrator in the target checkout
// and a final high-taste review.
//
// Usage:
//   dyna run examples/fanout-implementation.js --args '{
//     "task": "add structured logging to the CLI",
//     "repo": "/absolute/path/to/repo",
//     "maxTasks": 6,
//     "constraints": ["do not change public APIs"]
//   }'
//
// Warnings:
// - The target checkout must be clean. The script preflights git status
//   and refuses to scout, plan, or fan out on a dirty tree. Worktree
//   isolation starts at committed HEAD and does not copy uncommitted files.
// - Each isolated task ends with exactly one commit on the known clean
//   target HEAD. A repair amends that commit rather than adding another.
//   Integration verifies the one-commit range in the retained worktree, then
//   cherry-picks it. Do not trust an arbitrary commits[] list. Dyna never pushes.
// - maxTasks caps each independent wave (default 6, max 12). Fan-out is
//   also sized from profile.remaining / maxCallsPerRun so a repair wave
//   cannot exceed a capped profile. Leftover independent work and
//   planner-uncovered independent tasks share one second isolated wave
//   when budget remains; any remainder is reserved and reported.
// - Retained-worktree handoff selects the engine-authored worktrees() record
//   by the script's author-controlled agent label. Review and repair still
//   confirm git HEAD and base in that trusted cwd.
// - Isolated worktrees do not inherit one another. Dependent work is
//   applied only during integration, in planner order, in the target repo.
// - Implementer and reviewer must be distinct profiles.
export const meta = {
  name: 'fanout-implementation',
  description: 'Scout in parallel, plan ownership-bounded workstreams, implement in isolated worktrees, integrate accepted single commits',
  phases: [
    { title: 'Recon' },
    { title: 'Plan' },
    { title: 'Implement' },
    { title: 'Review' },
    { title: 'Integrate' },
    { title: 'Verdict' },
  ],
}

function listOf(xs) {
  if (typeof xs === 'string' && xs.trim()) return [xs.trim()]
  return (Array.isArray(xs) ? xs : [])
    .filter((x) => typeof x === 'string' && x.trim())
    .map((x) => x.trim())
}

function ctx(label, value) {
  return label + ' (prompt context, not a shell command): ' + JSON.stringify(value)
}

function asData(xs) {
  return listOf(xs)
    .map((s) => s.replace(/[\x00-\x1f\x7f]/g, '').trim())
    .filter(Boolean)
}

function sanitizeSha(s) {
  if (typeof s !== 'string') return ''
  const t = s.trim().toLowerCase()
  return /^[0-9a-f]{7,40}$/.test(t) ? t : ''
}

function shaEqual(a, b) {
  if (!a || !b) return false
  const n = Math.min(a.length, b.length)
  return n >= 7 && a.slice(0, n) === b.slice(0, n)
}

function normalizeAbsPath(p) {
  if (typeof p !== 'string') return ''
  const s = p.trim().replace(/\\/g, '/')
  if (!s || s.indexOf('\0') >= 0 || s.charAt(0) !== '/') return ''
  const parts = []
  const bits = s.split('/')
  for (let i = 0; i < bits.length; i++) {
    const raw = bits[i]
    if (!raw || raw === '.') continue
    if (raw === '..') return ''
    parts.push(raw)
  }
  return '/' + parts.join('/')
}

function normalizeOwned(p) {
  if (typeof p !== 'string') return ''
  const s = p.trim().replace(/\\/g, '/')
  if (!s || s.indexOf('\0') >= 0) return ''
  if (s.charAt(0) === '/' || /^[A-Za-z]:(\/|$)/.test(s)) return ''
  const parts = []
  const bits = s.split('/')
  for (let i = 0; i < bits.length; i++) {
    const raw = bits[i]
    if (!raw || raw === '.') continue
    if (raw === '..') return ''
    parts.push(raw)
  }
  return parts.join('/')
}

function pathsOverlap(a, b) {
  return a === b || a.indexOf(b + '/') === 0 || b.indexOf(a + '/') === 0
}

// Dyna creates isolated trees with os.MkdirTemp("", "dyna-wt-*"). The
// script never receives that path from the runtime — only from worker JSON.
function isDynaWorktreeRoot(pwd) {
  const p = normalizeAbsPath(pwd)
  if (!p) return false
  const parts = p.split('/').filter(Boolean)
  if (!parts.length) return false
  return /^dyna-wt-[A-Za-z0-9._-]+$/.test(parts[parts.length - 1])
}

function isIsolatedWorktree(pwd, repo) {
  const p = normalizeAbsPath(pwd)
  const r = normalizeAbsPath(repo)
  if (!p || !r || p === r) return false
  if (p.indexOf(r + '/') === 0) return false
  return isDynaWorktreeRoot(p)
}

function remainingOf(name) {
  const list = profiles || []
  for (let i = 0; i < list.length; i++) {
    if (list[i] && list[i].name === name) return list[i].remaining
  }
  return 0
}

function pick(stat, except) {
  const skip = except || {}
  const ranked = [...(profiles || [])]
    .filter((p) => p && typeof p[stat] === 'number' && !skip[p.name])
    .sort((a, b) => (b[stat] - a[stat]) || String(a.name).localeCompare(String(b.name)))
  if (!ranked.length) {
    const used = Object.keys(skip).filter((k) => skip[k])
    throw new Error(
      'no enabled profile with a ' + stat + ' stat' +
      (used.length ? ' distinct from ' + used.join(',') : ''))
  }
  return ranked[0].name
}

function acceptResult(impl, verdict) {
  return !!(verdict && verdict.accepted && impl && impl.commit && impl.tests && impl.tests.passed)
}

function needsRepair(impl, rev) {
  if (!impl || !impl.tests || !impl.tests.passed) return true
  if (!rev || !rev.accepted) return true
  return false
}

function repairIssues(impl, rev) {
  const issues = asData(rev && rev.issues)
  if (issues.length) return issues
  if (impl && impl.tests && !impl.tests.passed) return ['focused tests did not pass']
  if (rev && !rev.accepted) return ['review rejected without enumerated issues']
  return ['implementation needs a focused repair']
}

function normalizeTask(raw, i, fallbackPrefix) {
  const fallbackId = fallbackPrefix + '-' + (i + 1)
  if (typeof raw === 'string') {
    const title = raw.trim()
    return {
      id: fallbackId,
      title: title || fallbackId,
      ownedFiles: [],
      tests: [],
      dependsOn: [],
      notes: '',
    }
  }
  const obj = (raw && typeof raw === 'object' && !Array.isArray(raw)) ? raw : {}
  const id = (typeof obj.id === 'string' && obj.id.trim()) ? obj.id.trim() : fallbackId
  const title = (typeof obj.title === 'string' && obj.title.trim()) ? obj.title.trim() : id
  return {
    id,
    title,
    ownedFiles: listOf(obj.ownedFiles),
    tests: asData(obj.tests),
    dependsOn: listOf(obj.dependsOn),
    notes: typeof obj.notes === 'string' ? obj.notes : '',
  }
}

function partitionWork(tasks, glue, alreadyClaimed) {
  const glueNorm = []
  for (let i = 0; i < glue.length; i++) {
    const g = normalizeOwned(glue[i])
    if (g) glueNorm.push(g)
  }
  const claimed = []
  if (Array.isArray(alreadyClaimed)) {
    for (let i = 0; i < alreadyClaimed.length; i++) {
      const c = normalizeOwned(alreadyClaimed[i])
      if (c) claimed.push(c)
    }
  }
  const independent = []
  const reserved = []
  const list = Array.isArray(tasks) ? tasks : []
  for (let i = 0; i < list.length; i++) {
    const raw = list[i] || {}
    const id = (typeof raw.id === 'string' && raw.id.trim()) ? raw.id.trim() : ('task-' + (i + 1))
    const title = (typeof raw.title === 'string' && raw.title.trim()) ? raw.title.trim() : id
    const ownedFiles = []
    const rawOwned = listOf(raw.ownedFiles)
    let invalid = false
    for (let j = 0; j < rawOwned.length; j++) {
      const n = normalizeOwned(rawOwned[j])
      if (!n) { invalid = true; break }
      ownedFiles.push(n)
    }
    const item = {
      id, title, ownedFiles,
      tests: asData(raw.tests),
      dependsOn: listOf(raw.dependsOn),
      notes: typeof raw.notes === 'string' ? raw.notes : '',
    }
    let reason = ''
    if (invalid || !ownedFiles.length) reason = invalid ? 'invalid owned path' : 'no owned files'
    else if (item.dependsOn.length) reason = 'depends on other workstreams'
    else if (ownedFiles.some((f) => glueNorm.some((g) => pathsOverlap(f, g)))) reason = 'owns reserved glue files'
    else if (ownedFiles.some((f) => claimed.some((c) => pathsOverlap(f, c)))) reason = 'owned files overlap another workstream'
    if (reason) {
      reserved.push(Object.assign({}, item, { reason }))
      continue
    }
    ownedFiles.forEach((f) => { claimed.push(f) })
    independent.push(item)
  }
  return { independent, reserved, claimed }
}

function worktreeReport(impl, repo, expectedBase, retainedPath) {
  if (!impl || !isIsolatedWorktree(retainedPath, repo)) {
    throw new Error('engine-authored retained path is not a Dyna worktree root')
  }
  const pwd = normalizeAbsPath(retainedPath)
  const tip = sanitizeSha(impl.commit)
  const head = sanitizeSha(impl.head)
  const base = sanitizeSha(impl.base)
  if (!tip || !head || !shaEqual(tip, head)) {
    throw new Error('commit is not the isolated worktree HEAD')
  }
  if (!base || !expectedBase || !shaEqual(base, expectedBase)) {
    throw new Error('isolated base is not the known clean target HEAD')
  }
  if (shaEqual(tip, base)) {
    throw new Error('isolated task must end with exactly one commit on the clean target HEAD')
  }
  return {
    pwd, commit: tip, head, base,
    files: listOf(impl.files).map(normalizeOwned).filter(Boolean),
    tests: impl.tests,
    summary: typeof impl.summary === 'string' ? impl.summary : '',
  }
}

function fanoutBudget(wanted) {
  const intel = remainingOf(builder)
  const taste = remainingOf(reviewer)
  const intelCap = (intel == null) ? wanted : Math.floor(Math.max(0, intel - 1) / 2)
  const tasteCap = (taste == null) ? wanted : Math.floor(Math.max(0, taste - 1) / 2)
  return Math.max(0, Math.min(wanted, maxTasks, intelCap, tasteCap))
}

function finalizeIntegration(raw) {
  const dirty = !!(raw && raw.dirtyTarget)
  const suitePassed = !!(raw && raw.suite && raw.suite.passed)
  const claimed = !!(raw && raw.committed)
  const committed = claimed && !dirty && suitePassed
  const commit = committed ? sanitizeSha(raw && raw.commit) : ''
  return Object.assign({}, raw || {}, { committed, commit, dirtyTarget: dirty })
}

function summarizeDropped(items, rows) {
  const out = []
  for (let i = 0; i < items.length; i++) {
    if (!rows[i]) out.push({ id: items[i].id, title: items[i].title, reason: 'pipeline dropped' })
  }
  return out
}

const input = (args && typeof args === 'object' && !Array.isArray(args)) ? args : {}
const task = (typeof input.task === 'string' && input.task.trim())
  ? input.task.trim()
  : 'implement the requested repository change'
const repoIn = (typeof input.repo === 'string' && input.repo.trim())
  ? input.repo.trim()
  : process.cwd()
const repo = normalizeAbsPath(repoIn.charAt(0) === '/'
  ? repoIn
  : (String(process.cwd()).replace(/\/+$/, '') + '/' + repoIn.replace(/^(\.\/)+/, '')))
if (!repo) throw new Error('repo must be an absolute path inside the filesystem root')
const maxRaw = Number(input.maxTasks)
const maxTasks = Math.max(1, Math.min(12, Number.isFinite(maxRaw) ? Math.floor(maxRaw) : 6))
const constraints = asData(input.constraints)

const scout = pick('cost')
const builder = pick('intelligence')
const reviewer = pick('taste', { [builder]: true })

const PREFLIGHT = {
  type: 'object', required: ['clean', 'head'],
  properties: {
    clean: { type: 'boolean' },
    head: { type: 'string' },
    status: { type: 'string' },
  },
}
const RECON_ARCH = {
  type: 'object', required: ['architecture', 'risks'],
  properties: {
    architecture: { type: 'string' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}
const RECON_TESTS = {
  type: 'object', required: ['focused', 'broad'],
  properties: {
    focused: { type: 'array', items: { type: 'string' } },
    broad: { type: 'array', items: { type: 'string' } },
  },
}
const RECON_BOUNDS = {
  type: 'object', required: ['boundaries', 'risks'],
  properties: {
    boundaries: { type: 'array', items: { type: 'string' } },
    risks: { type: 'array', items: { type: 'string' } },
  },
}
const TASK_SHAPE = {
  type: 'object', required: ['id', 'title', 'ownedFiles'],
  properties: {
    id: { type: 'string' }, title: { type: 'string' },
    ownedFiles: { type: 'array', items: { type: 'string' } },
    tests: { type: 'array', items: { type: 'string' } },
    dependsOn: { type: 'array', items: { type: 'string' } },
    notes: { type: 'string' },
  },
}
const PLAN = {
  type: 'object', required: ['tasks', 'integration'],
  properties: {
    rationale: { type: 'string' },
    tasks: { type: 'array', items: TASK_SHAPE },
    integration: { type: 'object', required: ['glueFiles', 'suite'], properties: {
      glueFiles: { type: 'array', items: { type: 'string' } },
      suite: { type: 'array', items: { type: 'string' } },
      notes: { type: 'string' },
    }},
    uncovered: { type: 'array', items: TASK_SHAPE },
  },
}
const IMPLEMENTED = {
  type: 'object', required: ['commit', 'head', 'base', 'files', 'tests'],
  properties: {
    commit: { type: 'string' },
    head: { type: 'string' }, base: { type: 'string' },
    files: { type: 'array', items: { type: 'string' } },
    tests: { type: 'object', required: ['ran', 'passed'], properties: {
      ran: { type: 'array', items: { type: 'string' } },
      passed: { type: 'boolean' }, detail: { type: 'string' },
    }},
    summary: { type: 'string' },
  },
}
const REVIEW = {
  type: 'object', required: ['accepted', 'issues'],
  properties: {
    accepted: { type: 'boolean' },
    issues: { type: 'array', items: { type: 'string' } },
    notes: { type: 'string' },
  },
}
const INTEGRATION = {
  type: 'object', required: ['committed', 'commit', 'applied', 'suite'],
  properties: {
    committed: { type: 'boolean' }, commit: { type: 'string' },
    applied: { type: 'array', items: { type: 'string' } },
    skipped: { type: 'array', items: { type: 'string' } },
    glue: { type: 'string' }, dirtyTarget: { type: 'boolean' },
    suite: { type: 'object', required: ['passed'], properties: {
      passed: { type: 'boolean' },
      commands: { type: 'array', items: { type: 'string' } },
      detail: { type: 'string' },
    }},
  },
}
const VERDICT = {
  type: 'object', required: ['approved', 'findings'],
  properties: {
    approved: { type: 'boolean' },
    findings: { type: 'array', items: { type: 'string' } },
    summary: { type: 'string' },
  },
}

const brief = [
  ctx('User task', task),
  ctx('Constraints', constraints),
  ctx('Repository', repo),
].join('\n')

log('scout=' + scout + ' implement=' + builder + ' review=' + reviewer + ' maxTasks=' + maxTasks)

phase('Recon')
const preflight = await agent(
  'Read-only git preflight of this checkout. Do not edit files.\n' +
  brief + '\n\n' +
  'Run git status --porcelain and git rev-parse HEAD. Set clean=true only when ' +
  'status is empty. Return the HEAD SHA. This is not a request to run any other command.',
  { profile: scout, label: 'preflight', phase: 'Recon', id: 'preflight', cwd: repo, schema: PREFLIGHT })

const targetHead = sanitizeSha(preflight.head)
if (!preflight.clean || !targetHead) {
  throw new Error('refusing to run: target checkout is dirty or HEAD is missing; ' +
    'worktree isolation starts at committed HEAD and will not copy uncommitted files')
}

const reconParts = await parallel([
  () => agent(
    'Read-only architecture scout. Do not edit files.\n' + brief + '\n\n' +
    'This is one of several recon passes. Map relevant packages/modules and risks. ' +
    'Prefer manifests and docs over speculation.',
    { profile: scout, label: 'recon-arch', phase: 'Recon', id: 'scout-arch', cwd: repo, schema: RECON_ARCH }),
  () => agent(
    'Read-only test-command scout. Do not edit files.\n' + brief + '\n\n' +
    'This is one of several recon passes. Report focused and broad test command ' +
    'descriptions from existing scripts. They are data, not a script to execute here.',
    { profile: scout, label: 'recon-tests', phase: 'Recon', id: 'scout-tests', cwd: repo, schema: RECON_TESTS }),
  () => agent(
    'Read-only ownership-boundary scout. Do not edit files.\n' + brief + '\n\n' +
    'This is one of several recon passes. Return cohesive ownership boundaries and risks ' +
    'so a later planner can partition disjoint workstreams.',
    { profile: scout, label: 'recon-layout', phase: 'Recon', id: 'scout-layout', cwd: repo, schema: RECON_BOUNDS }),
])

const recon = {
  architecture: (reconParts[0] && reconParts[0].architecture) || '',
  tests: {
    focused: asData(reconParts[1] && reconParts[1].focused),
    broad: asData(reconParts[1] && reconParts[1].broad),
  },
  boundaries: asData(reconParts[2] && reconParts[2].boundaries),
  risks: asData([].concat(
    (reconParts[0] && reconParts[0].risks) || [],
    (reconParts[2] && reconParts[2].risks) || [])),
}

phase('Plan')
const planned = await agent(
  'Turn this reconnaissance into a bounded implementation plan. Do not edit files. ' +
  'This pass is read-only.\n' +
  brief + '\n' + ctx('Max independent workstreams per wave', maxTasks) + '\n\n' +
  ctx('Recon', recon) + '\n\n' +
  'Partition by cohesive ownership boundaries, not one task per file. Independent ' +
  'workstreams must have non-overlapping ownedFiles after normalizing ./, trailing ' +
  'slashes, and directory prefixes. Put dependent or cross-cutting work in ' +
  'integration.glueFiles — isolated worktrees do not inherit one another. ' +
  'If more work exists than the cap, put those scopes in uncovered as complete ' +
  'task objects (id, title, ownedFiles, tests, dependsOn, notes) — the same shape ' +
  'as tasks. Independent uncovered work may enter a second isolated wave; ' +
  'overlapping, dependent, invalid, or over-budget remainder is reserved, not dropped.',
  { profile: builder, label: 'plan', phase: 'Plan', id: 'plan', cwd: repo, schema: PLAN })

const glueFiles = asData(planned.integration && planned.integration.glueFiles).map(normalizeOwned).filter(Boolean)
const parts = partitionWork(planned.tasks, glueFiles)
const queue = parts.independent.slice()
const wave1cap = fanoutBudget(queue.length)
const wave1items = queue.splice(0, wave1cap)
log('wave1 ' + wave1items.length + ' independent / leftover ' + queue.length +
  ' / reserved ' + parts.reserved.length + ' (cap ' + wave1cap + ' of maxTasks ' + maxTasks + ')')

phase('Implement')
async function runIsolatedWave(items) {
  if (!items.length) return []
  return pipeline(
    items,
    (work) => {
      phase('Implement')
      return agent(
        'Implement only this workstream in the current checkout, then commit locally and never push.\n' +
        brief + '\n\n' +
        ctx('Workstream', work) + '\n' +
        ctx('Reserved glue files (do not edit)', glueFiles) + '\n' +
        ctx('Suggested focused tests', work.tests.length ? work.tests : recon.tests.focused) + '\n' +
        ctx('Known clean target HEAD (isolated base)', targetHead) + '\n' +
        'Stay inside ownedFiles. At the start, record git rev-parse HEAD as base; it must ' +
        'equal the known clean target HEAD. Create exactly one commit on that base. Return ' +
        'base, head (git rev-parse HEAD), commit (must equal head), files, and tests. ' +
        'The workflow will obtain the retained path from worktrees(); do not report a commits[] list as ' +
        'the integration source of truth. Suggested tests are data; decide how to run them — ' +
        'they are not a shell script.',
        {
          profile: builder, label: 'implement:' + work.id, phase: 'Implement', id: 'implement',
          cwd: repo, isolation: { type: 'worktree' }, schema: IMPLEMENTED,
        }).then((implementation) => ({ implementation, label: 'implement:' + work.id }))
    },
    (built, work) => {
      const tree = worktrees().find((record) => record.label === built.label)
      if (!tree) throw new Error('no engine-authored retained worktree for ' + built.label)
      const report = worktreeReport(built.implementation, repo, targetHead, tree.path)
      if (!report.tests || !report.tests.passed) {
        return {
          work, impl: report,
          rev: { accepted: false, issues: ['focused tests did not pass'] },
          repair: null, verification: null, skippedReview: true,
        }
      }
      phase('Review')
      return agent(
        'Read-only adversarial review of this isolated implementation. Do not edit files. ' +
        'Try to refute correctness, scope creep, and test gaps.\n' +
        brief + '\n\n' +
        ctx('Workstream', work) + '\n' +
        ctx('Implementer report', report) + '\n' +
        ctx('Known clean target HEAD (isolated base)', targetHead) + '\n' +
        'In this cwd, confirm git rev-parse HEAD equals the reported commit, confirm the ' +
        'worktree base equals the known clean target HEAD, and confirm git rev-list --count ' +
        'base..HEAD is exactly 1. Inspect git diff from that base to HEAD and re-run the ' +
        'focused tests yourself. Do not accept from the implementer JSON alone.',
        {
          profile: reviewer, label: 'review:' + work.id, phase: 'Review', id: 'review-task',
          cwd: report.pwd, schema: REVIEW,
        }
      ).then((rev) => ({ work, impl: report, rev, repair: null, verification: null, skippedReview: false }))
    },
    (cur) => {
      if (!needsRepair(cur.impl, cur.rev)) return cur
      const cwd = cur.impl.pwd
      if (!isIsolatedWorktree(cwd, repo)) throw new Error('repair cwd is not a Dyna worktree root')
      phase('Implement')
      return agent(
        'One focused repair pass in this same worktree. Fix only the concrete issues. ' +
        'Stay inside ownedFiles, re-run focused tests, and never push.\n' +
        brief + '\n\n' +
        ctx('Workstream', cur.work) + '\n' +
        ctx('Current report', cur.impl) + '\n' +
        ctx('Repair issues', repairIssues(cur.impl, cur.rev)) + '\n' +
        ctx('Known clean target HEAD (isolated base)', targetHead) + '\n' +
        'Amend the existing isolated commit rather than adding another, so this worktree ' +
        'still has exactly one commit on the known clean target HEAD. In this cwd, confirm ' +
        'git rev-parse HEAD and the isolated base before and after the amend. Return base, ' +
        'head, commit=head, files, and tests.',
        {
          profile: builder, label: 'repair:' + cur.work.id, phase: 'Implement', id: 'repair',
          cwd: cwd, schema: IMPLEMENTED,
        }
      ).then((repairRaw) => {
        const repair = worktreeReport(repairRaw, repo, targetHead, cwd)
        if (repair.pwd !== cwd) throw new Error('repair left the retained isolated worktree')
        return Object.assign({}, cur, { repair })
      })
    },
    (cur) => {
      if (cur.repair) {
        const cwd = cur.repair.pwd
        if (!isIsolatedWorktree(cwd, repo) || cwd !== cur.impl.pwd) {
          throw new Error('verify cwd is not the retained Dyna worktree root')
        }
        phase('Review')
        return agent(
          'Read-only verification of the repair. Do not edit files. Accept only if the ' +
          'concrete issues are fixed and focused tests still pass.\n' +
          brief + '\n\n' +
          ctx('Workstream', cur.work) + '\n' +
          ctx('Repair report', cur.repair) + '\n' +
          ctx('Prior issues', repairIssues(cur.impl, cur.rev)) + '\n' +
          ctx('Known clean target HEAD (isolated base)', targetHead) + '\n' +
          'In this cwd, confirm git rev-parse HEAD equals the reported commit, confirm the ' +
          'worktree base equals the known clean target HEAD, and confirm git rev-list --count ' +
          'base..HEAD is exactly 1 (the repair must have amended, not added a commit). ' +
          'Inspect git diff from that base to HEAD and re-run the focused tests yourself. ' +
          'Do not accept from the repair JSON alone.',
          {
            profile: reviewer, label: 'verify:' + cur.work.id, phase: 'Review', id: 'verify-task',
            cwd: cwd, schema: REVIEW,
          }
        ).then((verification) => Object.assign({}, cur, {
          verification,
          accepted: acceptResult(cur.repair, verification),
        }))
      }
      return Object.assign({}, cur, { accepted: acceptResult(cur.impl, cur.rev) })
    }
  )
}

const wave1 = await runIsolatedWave(wave1items)

const uncoveredTasks = (Array.isArray(planned.uncovered) ? planned.uncovered : [])
  .map((raw, i) => normalizeTask(raw, i, 'uncovered'))
const claimedForUncovered = parts.claimed.slice()
for (let i = 0; i < parts.reserved.length; i++) {
  const files = parts.reserved[i].ownedFiles || []
  for (let j = 0; j < files.length; j++) {
    if (files[j]) claimedForUncovered.push(files[j])
  }
}
const uncoveredParts = partitionWork(uncoveredTasks, glueFiles, claimedForUncovered)
for (let i = 0; i < uncoveredParts.independent.length; i++) {
  queue.push(uncoveredParts.independent[i])
}
const uncoveredIds = {}
for (let i = 0; i < uncoveredTasks.length; i++) uncoveredIds[uncoveredTasks[i].id] = true
const reservedUncovered = uncoveredParts.reserved.map((t) =>
  Object.assign({}, t, { reason: 'planner uncovered: ' + t.reason }))
const reservedBase = parts.reserved.concat(reservedUncovered)
const wave2cap = fanoutBudget(queue.length)
const wave2items = queue.splice(0, wave2cap)
if (wave2items.length) {
  log('wave2 ' + wave2items.length + ' leftover+uncovered independent (cap ' + wave2cap +
    '); still queued ' + queue.length)
}
const wave2 = await runIsolatedWave(wave2items)
const leftover = queue.map((t) => Object.assign({}, t, {
  reason: uncoveredIds[t.id] ? 'uncovered beyond budget after second wave' : 'beyond budget after second wave',
}))
const reserved = reservedBase.concat(leftover)
const uncoveredHeld = reserved.filter((t) => uncoveredIds[t.id])
const uncoveredEnqueued = wave2items.filter((t) => uncoveredIds[t.id]).map((t) => t.id)

const waveItems = wave1items.concat(wave2items)
const results = wave1.concat(wave2)
const completed = results.filter(Boolean)
const accepted = completed.filter((r) => r.accepted)
const failed = completed.filter((r) => !r.accepted).map((r) => ({
  id: r.work.id,
  title: r.work.title,
  reason: (r.verification && !r.verification.accepted)
    ? 'verification rejected'
    : ((r.rev && !r.rev.accepted) ? 'review rejected' : 'not accepted'),
  issues: repairIssues(r.impl, r.verification || r.rev),
}))
const dropped = summarizeDropped(wave1items, wave1).concat(summarizeDropped(wave2items, wave2))
log(accepted.length + '/' + waveItems.length + ' accepted, ' + failed.length + ' failed, ' +
  dropped.length + ' dropped, ' + reserved.length + ' reserved, ' +
  uncoveredEnqueued.length + ' uncovered in wave2, ' + uncoveredHeld.length + ' uncovered reserved')

const acceptedCommits = accepted.map((r) => {
  const src = r.repair || r.impl
  return {
    id: r.work.id,
    title: r.work.title,
    worktree: src.pwd,
    base: src.base,
    tip: src.commit,
    head: src.head,
    files: src.files,
  }
})

phase('Integrate')
const integration = finalizeIntegration(await agent(
  'Integrate accepted single-commit workstreams into this target checkout. Never push.\n' +
  brief + '\n\n' +
  'Require a clean git status; if dirty, refuse, set dirtyTarget=true, and do not commit.\n' +
  'For each accepted workstream, in that worktree cwd: confirm git rev-parse HEAD equals tip, ' +
  'confirm the isolated base equals the known clean target HEAD, and confirm ' +
  'git rev-list --count base..HEAD is exactly 1. Only then cherry-pick that single commit ' +
  '(git cherry-pick tip, or base..tip). Do not apply an arbitrary commits[] list. If the ' +
  'range is not exactly one commit, skip the workstream and record it in skipped. Worktree ' +
  'commits share the object store; do not merge worktrees and do not assume isolated trees ' +
  'inherit one another.\n' +
  'Then resolve reserved glue / dependent / uncovered work. Failed and pipeline-dropped ' +
  'workstreams are still missing scopes: record them in skipped and do not treat them as done.\n' +
  'Decide how to verify using the suite description; it is not a shell script. Set ' +
  'suite.passed from what you actually ran. Do not set committed=true if the tree is dirty ' +
  'or the suite failed.\n\n' +
  ctx('Accepted single commits', acceptedCommits) + '\n' +
  ctx('Known clean target HEAD', targetHead) + '\n' +
  ctx('Reserved / dependent / uncovered work', reserved) + '\n' +
  ctx('Failed workstreams (scopes still missing)', failed) + '\n' +
  ctx('Pipeline-dropped workstreams (scopes still missing)', dropped) + '\n' +
  ctx('Planner uncovered (normalized)', uncoveredTasks) + '\n' +
  ctx('Uncovered reserved (not fanned out)', uncoveredHeld) + '\n' +
  ctx('Glue files', glueFiles) + '\n' +
  ctx('Planner integration notes', planned.integration) + '\n' +
  ctx('Broad suite', asData((planned.integration && planned.integration.suite) || recon.tests.broad)),
  {
    profile: builder, label: 'integrate', phase: 'Integrate', id: 'integrate',
    cwd: repo, schema: INTEGRATION,
  }))

phase('Verdict')
const verdict = await agent(
  'Independent read-only review of the integrated target checkout. Do not edit files.\n' +
  brief + '\n\n' +
  ctx('Integration report', integration) + '\n' +
  'Coverage: ' + accepted.length + ' accepted / ' + failed.length + ' failed / ' +
  dropped.length + ' dropped / ' + reserved.length + ' reserved / ' +
  uncoveredEnqueued.length + ' uncovered in wave2 / ' +
  uncoveredHeld.length + ' uncovered reserved.\n' +
  'Inspect git diff / git log of the integrated change and judge glue and tests. ' +
  'Report concrete findings. Do not rubber-stamp the integration JSON.',
  { profile: reviewer, label: 'verdict', phase: 'Verdict', id: 'verdict', cwd: repo, schema: VERDICT })

log(verdict.approved ? 'final review approved' : 'final review found issues')

return {
  planning: {
    profiles: { scout, builder, reviewer },
    recon,
    plan: planned,
    independent: waveItems.map((t) => t.id),
    reserved,
    uncovered: uncoveredTasks,
    uncoveredIndependent: uncoveredEnqueued,
    uncoveredReserved: uncoveredHeld,
    waves: { first: wave1items.map((t) => t.id), second: wave2items.map((t) => t.id) },
  },
  coverage: {
    planned: (planned && Array.isArray(planned.tasks)) ? planned.tasks.length : 0,
    independent: waveItems.length,
    reserved: reserved.length,
    uncovered: uncoveredHeld.length,
    expected: waveItems.length,
    completed: completed.length,
    accepted: accepted.length,
    failed: failed.length,
    dropped: dropped.length,
    waves: 1 + (wave2items.length ? 1 : 0),
  },
  tasks: results,
  integration,
  verdict,
}
