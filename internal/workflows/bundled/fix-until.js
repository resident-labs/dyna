export const meta = { name: 'fix-until', description: 'Fix a failing check in an isolated worktree until it passes or stops progressing' }
if (!args || typeof args.command !== 'string' || !args.command.trim()) throw new Error('command is required')
if (args.maxRounds !== undefined && (!Number.isInteger(args.maxRounds) || args.maxRounds < 1)) throw new Error('maxRounds must be a positive integer')
const maxRounds = args.maxRounds === undefined ? 3 : args.maxRounds
const ranked = [...profiles].filter(p => p.remaining !== 0).sort((a, b) => b.intelligence - a.intelligence)
if (!ranked.length) throw new Error('fix-until requires an enabled profile')
let cwd = process.cwd()
let previous = null
let result
let state = 'max-rounds'
let rounds = 0
for (let i = 0; i <= maxRounds; i++) {
  result = await run(args.command, { cwd, check: false, label: 'check-' + i })
  if (result.code === 0) { state = 'green'; break }
  const failure = JSON.stringify([result.code, result.stdout, result.stderr])
  if (failure === previous) { state = 'no-progress'; break }
  if (i === maxRounds) break
  previous = failure
  if (i === 0) {
    const clean = await run(['git', 'status', '--porcelain'], { cwd, label: 'preflight' })
    if (clean.stdout.trim()) throw new Error('fix-until requires a clean checkout: isolation excludes uncommitted changes')
  }
  const label = 'fix-' + i
  await agent('Fix the failures from this check without pushing. Change only what is needed. Command: ' + JSON.stringify(args.command) + '\nFailure: ' + failure,
    { profile: ranked[0].name, label, cwd, ...(i === 0 ? { isolation: { type: 'worktree', retention: 'all' } } : {}) })
  rounds++
  if (i === 0) {
    const tree = worktrees().find(t => t.label === label)
    if (!tree) throw new Error('isolated worktree was not retained')
    cwd = tree.path
  }
}
const diff = await run(['git', 'diff', '--stat', 'HEAD'], { cwd, check: false, label: 'diff-summary' })
const status = await run(['git', 'status', '--short'], { cwd, check: false, label: 'status-summary' })
return { state, rounds, check: result, worktree: cwd, diff: diff.stdout, status: status.stdout }
