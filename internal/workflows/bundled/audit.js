export const meta = { name: 'audit', description: 'Audit areas and independently verify findings', args: [{ name: 'what', required: true, description: 'What to audit' }, { name: 'paths', required: true, description: 'Non-empty array of paths to audit' }] }
if (!args || typeof args.what !== 'string' || !args.what.trim()) throw new Error('what is required')
if (!Array.isArray(args.paths) || !args.paths.length || args.paths.some(p => typeof p !== 'string' || !p.trim())) throw new Error('paths must be a non-empty array of paths')
const ranked = [...profiles].filter(p => p.remaining !== 0).sort((a, b) => b.diagnostics - a.diagnostics)
if (ranked.length < 2) throw new Error('audit requires two enabled profiles')
const findingsSchema = { type: 'object', required: ['findings'], properties: { findings: { type: 'array', items: { type: 'object', required: ['file', 'line', 'claim', 'evidence'], properties: { file: { type: 'string' }, line: { type: 'integer' }, claim: { type: 'string' }, evidence: { type: 'string' } } } } } }
const verdictSchema = { type: 'object', required: ['confirmed', 'evidence'], properties: { confirmed: { type: 'boolean' }, evidence: { type: 'string' } } }
const scans = await parallel(args.paths.map((path, i) => () => agent(
  'Read-only audit of ' + JSON.stringify(path) + ' for ' + JSON.stringify(args.what) +
  '. Inspect source and relevant callers. Do not run builds or tests. Report only actionable defects with exact file, line, claim, and concrete evidence. Do not edit.',
  { profile: ranked[0].name, label: 'audit-' + i, schema: findingsSchema })))
const candidates = scans.flatMap(s => s ? s.findings : [])
const checks = await parallel(candidates.map((finding, i) => () => agent(
  'Adversarially verify this alleged defect independently by reading source and callers. Do not edit, build, or test. Try to refute it; confirm only if evidence proves the defect and its impact. Finding: ' + JSON.stringify(finding),
  { profile: ranked[1].name, label: 'verify-' + i, schema: verdictSchema })))
return { confirmed: candidates.map((finding, i) => ({ finding, verdict: checks[i] })).filter(row => row.verdict && row.verdict.confirmed), incompleteAreas: args.paths.filter((_, i) => !scans[i]), unverifiedCount: checks.filter(c => !c).length }
