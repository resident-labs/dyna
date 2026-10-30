export const meta = { name: 'deep-research', description: 'Research a question and independently verify cited claims', args: [{ name: 'question', required: true, description: 'Question to research' }] }
if (typeof args === 'string') args = { question: args }
if (!args || typeof args.question !== 'string' || !args.question.trim()) throw new Error('question is required')
const ranked = [...profiles].filter(p => p.remaining !== 0).sort ( (a, b) => b.diagnostics - a.diagnostics)
if (ranked.length < 2) throw new Error('deep-research requires two enabled profiles')
const question = args.question.trim()
const angles = ['primary sources and direct evidence', 'independent perspectives and counterarguments', 'recent developments and limitations']
const research = await parallel(angles.map((angle, i) => () => agent(
  'Research this question: ' + JSON.stringify(question) + '\nAngle: ' + angle +
  '\nUse web_search and fetch the relevant sources. Distinguish primary evidence from commentary. Return specific claims, exact source URLs, dates, and uncertainties. Do not invent sources.',
  { profile: ranked[0].name, label: 'research-' + i })))
const verification = await parallel(research.map((report, i) => () => agent(
  'Independently verify this research for the question ' + JSON.stringify(question) +
  '. Use web_search and fetch sources yourself; do not trust the draft. Check each material claim against sources, find contrary evidence, and flag inaccessible sources. Return only supported claims with source URLs, confidence, and open gaps. Draft: ' + JSON.stringify(report),
  { profile: ranked[1].name, label: 'verify-' + i })))
const report = await agent('Write a coherent cited report answering ' + JSON.stringify(question) + '. Use only independently verified claims below, each with source URLs and confidence. Explain disagreements, failed verification, and open gaps. Do not invent sources. Verification: ' + JSON.stringify(verification), { profile: ranked[1].name, label: 'report' })
return { question, report, verification, incomplete: research.some(r => !r) || verification.some(v => !v) }
