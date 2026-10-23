// Names for the profiles the composer offers after "/". A profile stores
// slugs ("pi", "deepseek/deepseek-flash"); a person reads "Pi · DeepSeek
// Flash", and two profiles on one model still read apart.
import type { ProfileChoice } from '../api/room.ts'

// SHOWN_AS fixes the names that plain capitalisation gets wrong.
const SHOWN_AS: Record<string, string> = {
  gpt: 'GPT',
  glm: 'GLM',
  mimo: 'MiMo',
  deepseek: 'DeepSeek',
  opencode: 'OpenCode',
  chatgpt: 'ChatGPT',
}

// words splits a slug into its words and drops a vendor prefix:
// "opencode-go/mimo-v2.6-pro" gives "mimo v2.6 pro".
function words(slug: string | undefined): string[] {
  const leaf = (slug ?? '').split('/').pop() ?? ''
  return leaf.split(/[-_\s]+/).filter(Boolean)
}

// word writes one word as a name. A version keeps its own shape ("3.8",
// "v2.6"), and a known name keeps its casing ("GLM", "MiMo").
function word(part: string): string {
  const known = SHOWN_AS[part.toLowerCase()]
  if (known) return known
  if (/^v?\d/.test(part)) return part
  return part.charAt(0).toUpperCase() + part.slice(1).toLowerCase()
}

function label(slug: string | undefined): string {
  return words(slug).map(word).join(' ')
}

// modelLabel reads a model slug as a name: "gpt-6.1-sol" is "GPT 6.1 Sol".
export function modelLabel(model: string | undefined): string {
  return label(model)
}

// tier lists the words of a profile name that its model name does not have,
// so profiles that share a model still read apart: "sol-ultrafast" adds
// "Ultrafast" to "GPT 6.1 Sol". A profile named after its model adds none.
function tier(name: string, model: string | undefined): string[] {
  const known = new Set(words(model).map((w) => w.toLowerCase()))
  return words(name).filter((w) => !known.has(w.toLowerCase())).map(word)
}

// profileHint is the note beside "/<profile>": the harness, the model as a
// name, the tier when the name adds one, and the default mark.
export function profileHint(profile: ProfileChoice): string {
  const parts = [label(profile.harness), modelLabel(profile.model), ...tier(profile.name, profile.model)]
  if (profile.default) parts.push('default')
  return parts.filter(Boolean).join(' · ')
}
