import { isDir, type RepoEntry } from '../files/repo.ts'

// The composer mention choices: one hint
// per choice, and the prefix match both lists use.
export interface MentionOption {
  value: string
  hint: string
}

// The fields the mention list reads from a member.
export interface MentionMember {
  name: string
  owner?: boolean
  closed?: boolean
}

export function mentionOptions(choices: readonly string[]): MentionOption[] {
  return choices.map((c) => ({ value: c, hint: c === '@root' ? 'external orchestrators watching this room' : c === '@team' ? 'your top-level sessions' : c.includes(':') ? 'another room' : 'member' }))
}

export function matchMentions<T extends MentionOption>(options: readonly T[], text: string, limit = 8): T[] {
  const q = text.toLowerCase()
  return options.filter((o) => o.value.toLowerCase().startsWith(q)).slice(0, limit)
}

// Live member items contain the open roster needed for mention choices.
export function memberChoices(members: readonly MentionMember[]): string[] {
  return ['@team', '@root', ...members.filter((member) => !member.owner && !member.closed && member.name !== 'root' && member.name !== 'team').map((member) => '@' + member.name).sort()]
}

export function fileMentionDirectory(text: string): string {
  const path = text.slice(1)
  return path.slice(0, Math.max(0, path.lastIndexOf('/')))
}

export function fileMentionOptions(entries: readonly RepoEntry[], text: string) {
  const query = text.slice(1).toLowerCase()
  return [...entries].sort((a, b) => Number(isDir(b)) - Number(isDir(a)) || a.name.localeCompare(b.name))
    .filter(entry => entry.path.toLowerCase().startsWith(query)).slice(0, 8)
    .map(file => ({ value: '@' + file.path + (isDir(file) ? '/' : ''), label: file.path + (isDir(file) ? '/' : ''), hint: isDir(file) ? 'Browse folder' : 'Insert file path', group: 'Files', file }))
}

// Inline code keeps a file path out of the room recipient parser. Use a
// longer code fence when a filename itself contains backticks.
export function fileMentionText(entry: RepoEntry): string {
  if (isDir(entry)) return '@' + entry.path + '/'
  const runs = entry.path.match(/`+/g) ?? []
  const fence = '`'.repeat(Math.max(0, ...runs.map(run => run.length)) + 1)
  const pad = entry.path.startsWith('`') || entry.path.endsWith('`') ? ' ' : ''
  return fence + pad + entry.path + pad + fence + ' '
}
