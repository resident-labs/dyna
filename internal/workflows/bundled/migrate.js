export const meta = { name: 'migrate', description: 'Transform matching files in isolated worktrees and verify each result' }
if (!args || typeof args.instruction !== 'string' || !args.instruction.trim()) throw new Error('instruction is required')
if (typeof args.glob !== 'string' || !args.glob.trim()) throw new Error('glob is required')
const ranked = [...profiles].filter(p => p.remaining !== 0).sort((a, b) => b.intelligence - a.intelligence)
if (ranked.length < 2) throw new Error('migrate requires two enabled profiles')
const expanded = await run(['python3', '-c', 'import glob,json,sys; print(json.dumps(sorted(p for p in glob.glob(sys.argv[1], recursive=True) if __import__("os").path.isfile(p))))', args.glob], { parse: 'json', label: 'expand' })
const files = expanded.json
const results = await parallel(files.map((file, i) => async () => {
  const label = 'transform-' + i
  const transformed = await agent('Apply this migration only to the specified file. Do not push or edit other files. Instruction: ' + JSON.stringify(args.instruction) + '\nFile: ' + JSON.stringify(file) + '\nReport what changed and any uncertainty.',
    { profile: ranked[0].name, label, isolation: { type: 'worktree', retention: 'all' } })
  const tree = worktrees().find(t => t.label === label)
  if (!tree) throw new Error('isolated worktree was not retained for ' + file)
  const verdict = await agent('Independently review the migration in this worktree. Inspect the diff and source. Check the instruction, correctness, and unintended changes. Do not edit. Report pass or fail with evidence. Instruction: ' + JSON.stringify(args.instruction) + '\nFile: ' + JSON.stringify(file),
    { profile: ranked[1].name, label: 'verify-' + i, cwd: tree.path, schema: { type: 'object', required: ['passed', 'evidence'], properties: { passed: { type: 'boolean' }, evidence: { type: 'string' } } } })
  return { file, status: verdict.passed ? 'verified' : 'rejected', worktree: tree.path, transformed, verdict }
}))
return { results: files.map((file, i) => results[i] || { file, status: 'failed' }) }
